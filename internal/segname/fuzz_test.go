package segname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParseFamily checks the invariants that make ordering trustworthy, over
// whatever a filesystem might hand us.
//
// Names on disk are attacker-influenced in the cases that matter: an evidence
// directory can hold anything, and a name that parses to the wrong segment
// number would silently reorder a device rather than fail to open it.
func FuzzParseFamily(f *testing.F) {
	for _, seed := range []string{
		".E01", ".E99", ".EAA", ".EZZ", ".e01", ".eaa", ".Ex01", ".EX01",
		".L01", ".Lx01", ".s01", ".FAA", ".ZZZ", ".E00", ".E0A", ".EA1",
		".txt", ".md5", ".log", ".zip", ".", "", "E01", ".E1", ".E001",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, ext string) {
		family, number, ok := ParseFamily(ext)
		if !ok {
			return
		}

		// A parsed name is a segment, and segment numbering starts at 1.
		if number == 0 {
			t.Fatalf("ParseFamily(%q) yielded segment 0, which does not exist", ext)
		}
		if !family.Valid() {
			t.Fatalf("ParseFamily(%q) yielded an invalid family %q", ext, family)
		}

		// The name must round-trip through the number: if it did not, two
		// spellings of one segment could order differently.
		canonical, err := family.Ext(number)
		if err != nil {
			t.Fatalf("ParseFamily(%q) = %d, but Ext(%d) failed: %v", ext, number, number, err)
		}
		if got, ok := family.Number(canonical); !ok || got != number {
			t.Fatalf("Ext(%d) = %q, which reads back as %d (ok=%v)", number, canonical, got, ok)
		}
		if !strings.EqualFold(canonical, ext) {
			t.Fatalf("ParseFamily(%q) = %d, but that segment is spelled %q", ext, number, canonical)
		}

		// The family must recognise its own member, and agree on the number.
		if got, ok := family.Number(ext); !ok || got != number {
			t.Fatalf("Family(%q).Number(%q) = %d (ok=%v), want %d", family, ext, got, ok, number)
		}
	})
}

// FuzzFamilyNumberOrdering pins the property the package exists for: the
// ordering of segment numbers is the ordering of the segments, whatever the
// names look like. A comparison that disagreed would assemble a device out of
// order, which decodes cleanly and is not the evidence.
func FuzzFamilyNumberOrdering(f *testing.F) {
	f.Add(".E99", ".EAA")
	f.Add(".E01", ".E02")
	f.Add(".EZZ", ".FAA")
	f.Add(".e02", ".E99")
	f.Add(".Ex01", ".ExAA")

	f.Fuzz(func(t *testing.T, left, right string) {
		family, _, ok := ParseFamily(left)
		if !ok {
			return
		}
		leftNumber, leftOK := family.Number(left)
		rightNumber, rightOK := family.Number(right)
		if !leftOK || !rightOK {
			return
		}

		// Equal numbers mean the same segment, so the names must differ only in
		// case; anything else would be two files claiming one position.
		if leftNumber == rightNumber && !strings.EqualFold(left, right) {
			t.Fatalf("%q and %q are both segment %d of a %q set", left, right, leftNumber, family)
		}

		// Regenerating both from their numbers must preserve the ordering.
		if leftNumber < rightNumber {
			leftExt, err := family.Ext(leftNumber)
			if err != nil {
				t.Fatalf("Ext(%d): %v", leftNumber, err)
			}
			rightExt, err := family.Ext(rightNumber)
			if err != nil {
				t.Fatalf("Ext(%d): %v", rightNumber, err)
			}
			if a, _ := family.Number(leftExt); a != leftNumber {
				t.Fatalf("Ext(%d) = %q does not read back", leftNumber, leftExt)
			}
			if b, _ := family.Number(rightExt); b != rightNumber {
				t.Fatalf("Ext(%d) = %q does not read back", rightNumber, rightExt)
			}
		}
	})
}

// FuzzDiscover checks that discovery reports only what is really there, over
// directories full of names chosen to look like segments.
func FuzzDiscover(f *testing.F) {
	f.Add("disk.E01\ndisk.E02\ndisk.E04\ndisk.log", 0)
	f.Add("disk.L01\ndisk.log\ndisk.lst", 0)
	f.Add("disk.E01\nDISK.e02\ndisk.E01.md5", 0)
	f.Add("disk.Ex01\ndisk.E01\ndisk.L01", 0)
	f.Add("disk\ndisk.\ndisk.E01", 2)

	f.Fuzz(func(t *testing.T, listing string, pick int) {
		dir := t.TempDir()

		var created []string
		for _, name := range strings.Split(listing, "\n") {
			// Keep the fuzzer inside one directory: it is discovery being
			// tested, not the filesystem's opinion of a hostile path.
			if name == "" || name == "." || name == ".." ||
				strings.ContainsAny(name, `/\:*?"<>|`) || len(name) > 64 {
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
				continue
			}
			created = append(created, name)
		}
		if len(created) == 0 {
			return
		}

		first := filepath.Join(dir, created[((pick%len(created))+len(created))%len(created)])
		set, err := Discover(first)
		if err != nil {
			return
		}

		if len(set.Paths()) != len(set.Segments) || len(set.Numbers()) != len(set.Segments) {
			t.Fatalf("%d segments render as %d paths and %d numbers",
				len(set.Segments), len(set.Paths()), len(set.Numbers()))
		}

		var found bool
		for i, segment := range set.Segments {
			if _, err := os.Stat(segment.Path); err != nil {
				t.Fatalf("discovered %s, which does not exist: %v", segment.Path, err)
			}
			if i > 0 && segment.Number <= set.Segments[i-1].Number {
				t.Fatalf("segment numbers are not strictly ascending: %v", set.Numbers())
			}
			if strings.EqualFold(segment.Path, first) {
				found = true
			}
		}

		// The file the caller named is part of its own set. Losing it would
		// mean opening an image other than the one that was asked for.
		if !found {
			t.Fatalf("Discover(%s) returned %v, which does not include the path it was given",
				first, set.Paths())
		}

		// Missing segments are holes below the highest number found, so they
		// can never overlap what was discovered.
		present := make(map[uint32]struct{}, len(set.Segments))
		for _, segment := range set.Segments {
			present[segment.Number] = struct{}{}
		}
		for _, number := range set.Missing() {
			if _, clash := present[number]; clash {
				t.Fatalf("segment %d is reported both present and missing", number)
			}
		}
	})
}
