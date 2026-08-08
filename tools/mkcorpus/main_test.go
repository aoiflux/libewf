package main

import (
	"strings"
	"testing"
)

// These two decide what the corpus records as an image. A name wrongly taken
// for a segment gets copied into the corpus and asserted against; a set put in
// the wrong order records a manifest whose segment list is not the acquisition
// order. Both survive into the golden data, where they are much harder to see
// than they are here.

func TestIsSegmentFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "case1.E01", want: true},
		{path: "case1.E99", want: true},
		{path: "case1.EAA", want: true},
		{path: "case1.e01", want: true},
		{path: "case1.L01", want: true},
		{path: "case1.s01", want: true},
		{path: "case1.Ex01", want: true},
		{path: "case1.Lx01", want: true},
		{path: "/evidence/case1.E02", want: true},

		{path: "case1.E01.md5"},
		{path: "case1.txt"},
		{path: "case1.raw"},
		{path: "case1"},
		{path: "case1.E00"},
		{path: "case1.E1"},

		// ".log" is a well-formed letter-pair extension of the L family — it
		// would be segment 470 of a logical set — and nothing in the name alone
		// says otherwise. Resolving that needs the rest of the directory, which
		// a per-path predicate does not have; segname.Discover resolves it by
		// admitting letter pairs only once all 99 numeric segments exist.
		//
		// This is why runEwfacquire writes the acquisition log to the raw
		// directory rather than beside the segments. The expectation here is
		// deliberately the true behaviour: if it ever changes, the workaround
		// it justifies should be revisited rather than silently kept.
		{path: "case1.acq.log", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isSegmentFile(tt.path); got != tt.want {
				t.Errorf("isSegmentFile(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestSortSegments(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		want  []string
	}{
		{
			name:  "numeric",
			paths: []string{"case1.E03", "case1.E01", "case1.E02"},
			want:  []string{"case1.E01", "case1.E02", "case1.E03"},
		},
		{
			// The reason this does not sort by name: .E99 is segment 99 and
			// .EAA is segment 100, and past that the leading letter carries.
			name:  "past the numeric range",
			paths: []string{"case1.EAA", "case1.FAA", "case1.E99", "case1.EZZ"},
			want:  []string{"case1.E99", "case1.EAA", "case1.EZZ", "case1.FAA"},
		},
		{
			name:  "mixed case",
			paths: []string{"case1.e03", "case1.E01", "case1.E02"},
			want:  []string{"case1.E01", "case1.E02", "case1.e03"},
		},
		{
			name:  "v2 spelling",
			paths: []string{"case1.Ex03", "case1.Ex01", "case1.Ex02"},
			want:  []string{"case1.Ex01", "case1.Ex02", "case1.Ex03"},
		},
		{
			// Nothing to order by, so fall back to name order rather than
			// leaving the input arbitrary.
			name:  "not segments",
			paths: []string{"c.txt", "a.txt", "b.txt"},
			want:  []string{"a.txt", "b.txt", "c.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortSegments(tt.paths)
			if strings.Join(tt.paths, " ") != strings.Join(tt.want, " ") {
				t.Errorf("sortSegments() = %v, want %v", tt.paths, tt.want)
			}
		})
	}
}
