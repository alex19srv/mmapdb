// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Alex Syrnikov <alex19srv@gmail.com>

package mmapdb

import (
	"github.com/alex19srv/mmapdb/internal/allocator"
	"github.com/alex19srv/mmapdb/internal/mmapbuf"
)

type MmapDB struct {
	buffer mmapbuf.MmappedBuffer
	heap   *allocator.Heap
}

func New(filename string) *MmapDB {
	buffer, err := mmapbuf.New(filename)
	if err != nil {
		panic(err)
	}
	heap, err := allocator.Init(buffer.Buffer, 2*1024*4)
	if err != nil {
		panic(err)
	}

	return &MmapDB{buffer: buffer, heap: heap}
}

type Handler uint32

func (db *MmapDB) Alloc(size int) (Handler, error) {
	handle, err := db.heap.Alloc(size)
	if err != nil {
		return Handler(0), err
	}

	return Handler(handle), nil
}

func (db *MmapDB) Free(handle Handler) {
	db.heap.Free(allocator.Handle(handle))
}

// Do executes a function on a stored mmapdb slice
// The function should not store the slice because it may be invalidated by a call to Compact
// That's why the function do not return the internal slice, but get closure to work with it
func (db *MmapDB) Do(chunk Handler, worker func([]byte)) {
	slice := db.heap.Get(allocator.Handle(chunk))
	worker(slice)
}
