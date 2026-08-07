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
		fmt.Println("usage: go run ./examples/readat <image.E01>")
		os.Exit(1)
	}

	// OpenPath decodes a multi-segment set from its first segment, so this
	// reads the same way whether the image is one file or twenty.
	r, err := libewf.OpenPath(os.Args[1])
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
