// SPDX-License-Identifier: MIT
// Copyright 2026 Alex Syrnikov <alex19srv@gmail.com>

// Package slabheap implements malloc()/free() over a caller-provided []byte
// (typically an mmap'd file). Objects up to 4096 bytes. All allocator
// metadata lives inside the buffer itself; payloads are position-independent
// and are relocated by Compact().
package allocator

import (
	"encoding/binary"
	"errors"
	"sort"
)

const (
	MaxAlloc  = 4096       // largest allocatable payload, bytes
	superSize = 64         // superblock: magic, tableCap, reserved
	entrySize = 8          // handle-table entry: uint32 offset + uint32 size
	none      = ^uint32(0) // "no link" sentinel
	magic     = 0x31424C53 // "SLB1" little-endian
)

var (
	ErrNoSpace  = errors.New("mmapdb: out of space (call Compact() and retry)")
	ErrNoHandle = errors.New("mmapdb: handle table exhausted")
	ErrBadSize  = errors.New("mmapdb: size out of range")
	ErrBadImage = errors.New("mmapdb: not a valid heap image")
)

// Handle is a stable reference to a live object. It survives relocation by
// Compact() and re-opening of the file. If payload objects need to reference
// each other, store handles (not offsets) inside the payload.
type Handle uint32

type Heap struct {
	buf       []byte
	dataStart uint32   // first byte of the data area
	dataEnd   uint32   // one past the last usable byte
	top       uint32   // bump pointer: first never-allocated byte
	classes   []uint32 // ascending size-class sizes
	freeHead  []uint32 // per-class intrusive free list: head offset
	nHandles  uint32
	freeHnd   uint32 // head of the free-handle chain (dead table entries)
}

func le32(b []byte, off int) uint32       { return binary.LittleEndian.Uint32(b[off:]) }
func putle32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
func align16(x uint32) uint32             { return (x + 15) &^ 15 }

// buildClasses: 48 size classes from 16 to 4096. Worst-case rounding waste
// is ~12% (e.g. a 1025-byte request occupies a 1152-byte slot).
func buildClasses() []uint32 {
	var cs []uint32
	add := func(from, to, step int) {
		for s := from; s <= to; s += step {
			cs = append(cs, uint32(s))
		}
	}
	add(16, 256, 16)
	add(288, 512, 32)
	add(576, 1024, 64)
	add(1152, 2048, 128)
	add(2304, MaxAlloc, 256)
	return cs
}

func (h *Heap) classFor(size int) int {
	return sort.Search(len(h.classes), func(i int) bool { return h.classes[i] >= uint32(size) })
}

func (h *Heap) tablePos(i uint32) int { return superSize + int(i)*entrySize }

func (h *Heap) getEntry(i uint32) (off, size uint32) {
	p := h.tablePos(i)
	return le32(h.buf, p), le32(h.buf, p+4)
}

func (h *Heap) putEntry(i, off, size uint32) {
	p := h.tablePos(i)
	putle32(h.buf, p, off)
	putle32(h.buf, p+4, size)
}

// Init lays out an empty heap over buf. tableCap is the maximum number of
// simultaneously live objects (8 bytes of table per handle). buf must be at
// least MaxAlloc bytes. The image persists in buf (e.g. an mmap'd file).
func Init(buf []byte, tableCap uint32) (*Heap, error) {
	start := align16(superSize + tableCap*entrySize)
	if tableCap == 0 || int(start) > len(buf) || len(buf) < MaxAlloc {
		return nil, ErrBadImage
	}
	putle32(buf, 0, magic)
	putle32(buf, 4, tableCap)
	h := &Heap{
		buf: buf, dataStart: start, dataEnd: uint32(len(buf)), top: start,
		classes: buildClasses(), nHandles: tableCap, freeHnd: 0,
	}
	h.freeHead = make([]uint32, len(h.classes))
	for i := range h.freeHead {
		h.freeHead[i] = none
	}
	for i := uint32(0); i+1 < tableCap; i++ { // chain every handle as free
		h.putEntry(i, i+1, none)
	}
	h.putEntry(tableCap-1, none, none)
	return h, nil
}

// Open re-attaches to an existing image and rebuilds all allocator state by
// compacting once. This self-heals torn free-list state after a crash: the
// live table entries are the only trusted data; every hole is reclaimed.
func Open(buf []byte) (*Heap, error) {
	if len(buf) < superSize || le32(buf, 0) != magic {
		return nil, ErrBadImage
	}
	tableCap := le32(buf, 4)
	start := align16(superSize + tableCap*entrySize)
	if tableCap == 0 || int(start) > len(buf) {
		return nil, ErrBadImage
	}
	h := &Heap{
		buf: buf, dataStart: start, dataEnd: uint32(len(buf)), top: start,
		classes: buildClasses(), nHandles: tableCap, freeHnd: none,
	}
	h.freeHead = make([]uint32, len(h.classes))
	for i := range h.freeHead {
		h.freeHead[i] = none
	}
	// Rebuild the free-handle chain; stale chain pointers in dead entries
	// are ignored and overwritten.
	for i := h.nHandles; i > 0; i-- {
		if _, size := h.getEntry(i - 1); size == none {
			h.putEntry(i-1, h.freeHnd, none)
			h.freeHnd = i - 1
		}
	}
	h.Compact()
	return h, nil
}

// Alloc returns a handle to a block with at least size usable bytes.
// O(1). On ErrNoSpace, call Compact() and retry once.
func (h *Heap) Alloc(size int) (Handle, error) {
	if size < 0 || size > MaxAlloc {
		return 0, ErrBadSize
	}
	hn := h.freeHnd
	if hn == none {
		return 0, ErrNoHandle
	}
	nextH, _ := h.getEntry(hn)

	ci := h.classFor(size)
	var off uint32
	if head := h.freeHead[ci]; head != none {
		off = head
		h.freeHead[ci] = le32(h.buf, int(off)) // next link is stored in the free block itself
	} else {
		slot := h.classes[ci]
		if h.dataEnd-slot < h.top {
			return 0, ErrNoSpace // caller may Compact() and retry
		}
		off = h.top
		h.top += slot
	}

	h.freeHnd = nextH
	h.putEntry(hn, off, uint32(size))
	return Handle(hn), nil
}

// Get resolves a handle to the object's current bytes (len == requested size).
// The returned slice is invalidated by Compact() and by Free() of this
// handle; never hold it across allocator calls that may compact.
func (h *Heap) Get(hn Handle) []byte {
	off, size := h.getEntry(uint32(hn))
	if size == none {
		panic("slabheap: use of dead handle")
	}
	return h.buf[off : off+size]
}

// Size returns the payload size of a live handle.
func (h *Heap) Size(hn Handle) int {
	_, size := h.getEntry(uint32(hn))
	if size == none {
		panic("slabheap: use of dead handle")
	}
	return int(size)
}

// Free releases a block. O(1). Panics on double free, mirroring Go's
// slice-bounds semantics for programmer error.
func (h *Heap) Free(hn Handle) {
	off, size := h.getEntry(uint32(hn))
	if size == none {
		panic("slabheap: double free")
	}
	ci := h.classFor(int(size))
	putle32(h.buf, int(off), h.freeHead[ci]) // push block onto its class free list
	h.freeHead[ci] = off
	h.putEntry(uint32(hn), h.freeHnd, none) // push handle: size==none marks the entry dead
	h.freeHnd = uint32(hn)
}

// Compact slides all live blocks to the bottom of the data area, collapsing
// every hole into one contiguous free region, then resets the bump pointer
// and all free lists in O(1). Only possible because payloads are
// position-independent. Cost: O(live log live + live bytes).
// Stop the world: no Get() slices may be in use during the call.
func (h *Heap) Compact() {
	type obj struct {
		off, size uint32
		hn        Handle
	}
	live := make([]obj, 0, 1024)
	for i := uint32(0); i < h.nHandles; i++ {
		if off, size := h.getEntry(i); size != none {
			live = append(live, obj{off, size, Handle(i)})
		}
	}
	sort.Slice(live, func(a, b int) bool { return live[a].off < live[b].off })

	dst := h.dataStart
	for _, o := range live {
		if o.off != dst {
			copy(h.buf[dst:dst+o.size], h.buf[o.off:o.off+o.size]) // Go copy is memmove-safe
			h.putEntry(uint32(o.hn), dst, o.size)
		}
		dst += h.classes[h.classFor(int(o.size))] // advance by slot size, never by payload size
	}
	h.top = dst
	for i := range h.freeHead {
		h.freeHead[i] = none
	}
}

// LiveObjects returns the number of live handles (stat helper).
func (h *Heap) LiveObjects() (n uint32) {
	for i := uint32(0); i < h.nHandles; i++ {
		if _, size := h.getEntry(i); size != none {
			n++
		}
	}
	return n
}

// Reserved returns the bytes of data area currently reserved by live
// blocks and unreclaimed holes (i.e. everything not yet reclaimable by
// a fresh bump allocation). Reserved - sum(live payload sizes) is your
// current fragmentation overhead. Grows via bump allocation; drops to
// the packed live size after Compact().
func (h *Heap) Reserved() int { return int(h.top - h.dataStart) }
