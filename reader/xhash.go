package reader

import (
	"bytes"
	"encoding/hex"
	"encoding/xml"
	"io"
	"strings"

	"github.com/aoiflux/libewf/compression"
	"github.com/aoiflux/libewf/metadata"
)

// utf8BOM prefixes the XML sections libewf writes.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// parseXHashSection extracts acquisition digests from an "xhash" section.
//
// Unlike the fixed-layout "hash" and "digest" sections, xhash is a
// zlib-compressed XML document:
//
//	<?xml version="1.0" encoding="UTF-8"?>
//	<xhash>
//		<MD5>85f0fb6e...</MD5>
//		<SHA1>87084f2c...</SHA1>
//	</xhash>
//
// The EWF-X dialect records its SHA-1 only here, so without this the digest is
// invisible even though the image carries it.
//
// Digests already read from a binary section win: those have a fixed layout and
// cannot be ambiguous, so they are the more trustworthy source.
func parseXHashSection(data []byte, info *metadata.Info) {
	doc, ok := inflateXML(data)
	if !ok {
		return
	}

	md5Hex, sha1Hex := extractXMLDigests(doc)

	if !info.HasMD5Digest {
		if raw, err := hex.DecodeString(md5Hex); err == nil && len(raw) == len(info.MD5Digest) {
			copy(info.MD5Digest[:], raw)
			info.HasMD5Digest = true
		}
	}
	if !info.HasSHA1Digest {
		if raw, err := hex.DecodeString(sha1Hex); err == nil && len(raw) == len(info.SHA1Digest) {
			copy(info.SHA1Digest[:], raw)
			info.HasSHA1Digest = true
		}
	}
}

// inflateXML decompresses an XML section body, tolerating writers that store it
// uncompressed, and strips any byte-order mark so the decoder accepts it.
func inflateXML(data []byte) ([]byte, bool) {
	doc := data
	if out, err := compression.DecompressZlib(data); err == nil {
		doc = out
	} else if !bytes.Contains(data, []byte("<")) {
		// Neither inflatable nor plausibly XML.
		return nil, false
	}
	return bytes.TrimPrefix(doc, utf8BOM), true
}

// extractXMLDigests pulls MD5 and SHA-1 values out of an xhash document.
//
// Element names are matched case-insensitively: libewf writes <MD5> and <SHA1>,
// but the casing is not guaranteed across writers, and a case-sensitive struct
// decode would silently return nothing.
func extractXMLDigests(doc []byte) (md5Hex, sha1Hex string) {
	decoder := xml.NewDecoder(bytes.NewReader(doc))
	decoder.Strict = false

	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			// Truncated or malformed XML: keep whatever was already read.
			break
		}

		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}

		var target *string
		switch strings.ToLower(start.Name.Local) {
		case "md5":
			target = &md5Hex
		case "sha1":
			target = &sha1Hex
		default:
			continue
		}

		var value string
		if err := decoder.DecodeElement(&value, &start); err != nil {
			continue
		}
		*target = strings.ToLower(strings.TrimSpace(value))
	}
	return md5Hex, sha1Hex
}
