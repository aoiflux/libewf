package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/aoiflux/libewf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./examples/readat <segment.E01>")
		os.Exit(1)
	}

	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Println("open file error:", err)
		os.Exit(1)
	}
	defer f.Close()

	r, err := libewf.Open(f)
	if err != nil {
		fmt.Println("libewf open error:", err)
		os.Exit(1)
	}
	defer r.Close()

	buf := make([]byte, 64)
	n, err := r.ReadAt(buf, 0)
	if err != nil && err != io.EOF {
		fmt.Println("read error:", err)
		os.Exit(1)
	}

	fmt.Printf("read=%d bytes\n", n)
	fmt.Println(hex.Dump(buf[:n]))
}
