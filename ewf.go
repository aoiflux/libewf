package libewf

// Version information
const (
	// Version is the current library version.
	//
	// 0.2.0 corrects chunk-table decoding: releases up to and including
	// v0.1.1 consumed both the "table" section and its "table2" backup copy,
	// which doubled the chunk table and returned incorrect data for every
	// offset past the first chunk group of a real E01. Any result derived
	// from an earlier release should be recomputed.
	Version = "0.2.0"

	// Author information
	Author = "libewf contributors"
)
