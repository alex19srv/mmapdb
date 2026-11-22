package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"

	"github.com/alex19srv/mmapdb/internal/allocator"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "FATAL:", err)
		os.Exit(1)
	}
}

func check(h *allocator.Heap, live map[allocator.Handle][]byte) {
	for hn, want := range live {
		got := h.Get(hn)
		if !bytes.Equal(got, want) {
			panic(fmt.Sprintf("corruption: handle %d size %d", hn, len(want)))
		}
	}
}

func fill(h *allocator.Heap, hn allocator.Handle, tag byte) []byte {
	got := h.Get(hn)
	for i := range got {
		got[i] = byte(i*31) ^ tag ^ byte(hn)
	}
	return append([]byte(nil), got...)
}

func main() {
	fmt.Printf("Test started. It can cake about minute.\n")
	os.Stdout.Sync()

	rng := rand.New(rand.NewSource(1))

	// ---- Phase A: randomized churn on a 64 MB heap --------------------
	buf := make([]byte, 64<<20) // stand-in for the mmap'd file
	h, err := allocator.Init(buf, 1<<20)
	must(err)

	live := make(map[allocator.Handle][]byte)
	edgeSizes := []int{0, 1, 15, 16, 17, 255, 256, 257, 511, 512, 513,
		1023, 1024, 1025, 2047, 2048, 2049, 4095, 4096}

	compacts := 0
	spaceRetries := 0
	lastCompact := -1000
	for iter := 0; iter < 300000; iter++ {
		switch r := rng.Intn(1000); {
		case r < 590 || len(live) == 0:
			size := edgeSizes[rng.Intn(len(edgeSizes))]
			if rng.Intn(2) == 0 {
				size = rng.Intn(allocator.MaxAlloc + 1)
			}
			hn, err := h.Alloc(size)
			if errors.Is(err, allocator.ErrNoSpace) && iter-lastCompact >= 1000 {
				// Production policy: rate-limit compaction. If the heap is
				// genuinely full of live data, Compact() cannot help and
				// retrying it on every failure just thrashes.
				h.Compact()
				compacts++
				lastCompact = iter
				hn, err = h.Alloc(size)
				spaceRetries++
			}
			if err == nil {
				live[hn] = fill(h, hn, byte(iter))
			}
		case r < 950:
			for hn := range live { // verify-then-free a random live object
				if !bytes.Equal(h.Get(hn), live[hn]) {
					panic("corruption before free")
				}
				h.Free(hn)
				delete(live, hn)
				break
			}
		case r < 995:
			// no-op fill
		default:
			h.Compact()
			compacts++
			for i := 0; i < 64 && i < len(live); i++ { // sample-verify
				for hn := range live {
					if !bytes.Equal(h.Get(hn), live[hn]) {
						panic("corruption after compact")
					}
					break
				}
			}
		}
		if iter%40000 == 0 {
			check(h, live)
		}
	}
	check(h, live)

	liveBytes := 0
	for _, b := range live {
		liveBytes += len(b)
	}
	fmt.Printf("phase A ok: ops=300000 compacts=%d spaceRetries=%d live=%d liveBytes=%.1fMB reserved=%.1fMB overhead=%.1f%%\n",
		compacts, spaceRetries, len(live), float64(liveBytes)/1e6,
		float64(h.Reserved())/1e6, 100*float64(h.Reserved()-liveBytes)/float64(liveBytes+1))

	// ---- Phase B: saturation, ErrNoSpace -> Compact -> retry ----------
	buf2 := make([]byte, 4<<20)
	h2, err := allocator.Init(buf2, 4096)
	must(err)
	liveB := make(map[allocator.Handle]bool)
	for {
		hn, err := h2.Alloc(3000)
		if err != nil {
			if !errors.Is(err, allocator.ErrNoSpace) {
				must(err)
			}
			break
		}
		fill(h2, hn, 1)
		liveB[hn] = true
	}
	filled := len(liveB)
	if _, err := h2.Alloc(3000); !errors.Is(err, allocator.ErrNoSpace) {
		panic("expected ErrNoSpace")
	}
	h2.Compact()
	if _, err := h2.Alloc(3000); err == nil { // still full: only dead space is reclaimable
		panic("unexpected alloc success on full heap")
	}
	// free every 2nd object, then allocations must succeed after compact
	freed := 0
	idx := 0
	for hn := range liveB {
		if idx%2 == 0 {
			h2.Free(hn)
			delete(liveB, hn)
			freed++
		}
		idx++
	}
	okAfterCompact := 0
	for {
		hn, err := h2.Alloc(3000)
		if errors.Is(err, allocator.ErrNoSpace) {
			h2.Compact()
			hn, err = h2.Alloc(3000)
		}
		if err != nil {
			break
		}
		fill(h2, hn, 2)
		liveB[hn] = true
		okAfterCompact++
	}
	fmt.Printf("phase B ok: filled=%d freed=%d refilled_after_compact=%d\n", filled, freed, okAfterCompact)
	if okAfterCompact < freed-2 {
		panic("compaction did not reclaim freed slots")
	}

	// ---- Phase C: reopen the phase-A image ----------------------------
	h3, err := allocator.Open(buf)
	must(err)
	check(h3, live)
	extra := 0
	for i := 0; i < 20000; i++ {
		hn, err := h3.Alloc(rng.Intn(allocator.MaxAlloc + 1))
		if errors.Is(err, allocator.ErrNoSpace) {
			h3.Compact()
			hn, err = h3.Alloc(rng.Intn(allocator.MaxAlloc + 1))
		}
		if err != nil {
			break
		}
		live[hn] = fill(h3, hn, 0xEE)
		extra++
	}
	check(h3, live)
	fmt.Printf("phase C ok: reopened image, %d extra allocations survived\n", extra)

	// ---- Phase D: handle table exhaustion -----------------------------
	buf3 := make([]byte, 1<<20)
	h4, err := allocator.Init(buf3, 128)
	must(err)
	nh := 0
	var lastErr error
	for {
		_, err := h4.Alloc(16)
		if err != nil {
			lastErr = err
			break
		}
		nh++
	}
	if nh != 128 || !errors.Is(lastErr, allocator.ErrNoHandle) {
		panic(fmt.Sprintf("table exhaustion wrong: nh=%d err=%v", nh, lastErr))
	}
	fmt.Printf("phase D ok: ErrNoHandle after %d handles\n", nh)

	fmt.Println("ALL TESTS PASSED")
}

// // liveMap enumerates handles of a heap (test helper for phase B).
// func liveMap(h *allocator.Heap, _ int) map[allocator.Handle]bool {
// 	m := make(map[allocator.Handle]bool)
// 	for i := uint32(0); i < 4096; i++ {
// 		if h.Size(allocator.Handle(i)) > 0 {
// 			m[allocator.Handle(i)] = true
// 		}
// 	}
// 	return m
// }
