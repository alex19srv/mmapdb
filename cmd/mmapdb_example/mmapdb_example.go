// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Alex Syrnikov <alex19srv@gmail.com>

package main

import (
	"fmt"
	"unsafe"

	"github.com/alex19srv/mmapdb/pkg/mmapdb"
)

type Data struct {
	Info string
}

func main() {
	db := mmapdb.New("./local/test.db")
	data := Data{
		Info: "Hello, World!",
	}

	h, err := save(db, data)
	if err != nil {
		panic(err)
	}

	db.Do(h, func(b []byte) {
		l := *(*uint32)(unsafe.Pointer(&b[0]))
		fmt.Println("Data:", string(b[4:4+l]))
	})

	db.Free(h)
}

func save(db *mmapdb.MmapDB, data Data) (mmapdb.Handler, error) {
	h, err := db.Alloc(int(unsafe.Sizeof(int(0))) + len(data.Info))
	if err != nil {
		panic(err)
	}
	db.Do(h, func(b []byte) {
		*(*uint32)(unsafe.Pointer(&b[0])) = uint32(len(data.Info))
		copy(b[4:], data.Info)
	})

	return h, err
}
