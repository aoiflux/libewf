package compression

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"fmt"
	"io"
)

// Decompress decompresses data using the given EWF compression method.
// method 0 (none) returns a copy of the input unchanged.
// method 1 (deflate/zlib) uses compress/zlib.
// method 2 (bzip2) uses compress/bzip2.
func Decompress(data []byte, method uint16) ([]byte, error) {
	switch method {
	case 0:
		out := make([]byte, len(data))
		copy(out, data)
		return out, nil
	case 1:
		return decompressZlib(data)
	case 2:
		return decompressBzip2(data)
	default:
		return nil, fmt.Errorf("compression: unsupported method %d", method)
	}
}

// DecompressZlib decompresses zlib/deflate-encoded data. Used for EWF v1
// chunks where compression is always deflate regardless of the file-header field.
func DecompressZlib(data []byte) ([]byte, error) {
	return decompressZlib(data)
}

func decompressZlib(data []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("compression: zlib reader: %w", err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("compression: zlib decompress: %w", err)
	}
	return out, nil
}

func decompressBzip2(data []byte) ([]byte, error) {
	r := bzip2.NewReader(bytes.NewReader(data))
	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("compression: bzip2 decompress: %w", err)
	}
	return out, nil
}
