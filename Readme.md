## mmapdb

`mmapdb` - storage with file mapped in memory. Intended to use for data, which need to survive process restart.
Usage is simple:

- create new db
- allocate memory (this will return handler, not memory slice)
- write data (method Do will get worker closure to read/write data slice)
- read data (with Do method)
- free memory

Allocated memory can be moved in memory during compaction, so you need to use handler to access data with method `Do`.

### Example

```go
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
```
