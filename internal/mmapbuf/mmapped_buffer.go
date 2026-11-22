// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Alex Syrnikov <alex19srv@gmail.com>

package mmapbuf

import (
	"os"

	"golang.org/x/sys/unix"
)

type MmappedBuffer struct {
	Buffer []byte
}

func New(filename string) (MmappedBuffer, error) {
	const size = 2 * 1024 * 1024

	f, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	if err := f.Truncate(size); err != nil {
		panic(err)
	}
	mem, err := unix.Mmap(
		int(f.Fd()),
		0,
		size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_SHARED,
	)
	if err != nil {
		panic(err)
	}
	f.Close()

	return MmappedBuffer{Buffer: mem}, nil
}

func (m *MmappedBuffer) Sync() {
	err := unix.Msync(m.Buffer, unix.MS_ASYNC)
	if err != nil {
		panic(err)
	}
}

func (m *MmappedBuffer) Close() {
	err := unix.Munmap(m.Buffer)
	if err != nil {
		panic(err)
	}
	m.Buffer = nil
}
