package segname

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/aoiflux/libewf/ewferr"
)

func TestParseFamily(t *testing.T) {
	tests := []struct {
		ext    string
		base   byte
		v2     bool
		number uint32
		ok     bool
	}{
		{ext: ".E01", base: 'E', number: 1, ok: true},
		{ext: ".E02", base: 'E', number: 2, ok: true},
		{ext: ".E99", base: 'E', number: 99, ok: true},
		{ext: ".EAA", base: 'E', number: 100, ok: true},
		{ext: ".EAB", base: 'E', number: 101, ok: true},
		{ext: ".EAZ", base: 'E', number: 125, ok: true},
		{ext: ".EBA", base: 'E', number: 126, ok: true},
		{ext: ".EZZ", base: 'E', number: 775, ok: true},
		{ext: ".L01", base: 'L', number: 1, ok: true},
		{ext: ".s01", base: 's', number: 1, ok: true},
		{ext: ".Ex01", base: 'E', v2: true, number: 1, ok: true},
		{ext: ".Lx01", base: 'L', v2: true, number: 1, ok: true},
		{ext: ".ExAA", base: 'E', v2: true, number: 100, ok: true},

		// Case is not significant, because acquisition tools disagree about it
		// and the same set may be written either way.
		{ext: ".e01", base: 'e', number: 1, ok: true},
		{ext: ".eaa", base: 'e', number: 100, ok: true},
		{ext: ".EX01", base: 'E', v2: true, number: 1, ok: true},

		// A carried leading letter names no family on its own.
		{ext: ".FAA"},
		{ext: ".ZZZ"},

		// Not segments.
		{ext: ".E00"},
		{ext: ".E1"},
		{ext: ".E001"},
		{ext: ".E0A"},
		{ext: ".EA1"},
		{ext: ".txt"},
		{ext: ".md5"},
		{ext: "E01"},
		{ext: "."},
		{ext: ""},
	}

	for _, tt := range tests {
		t.Run(tt.ext, func(t *testing.T) {
			family, number, ok := ParseFamily(tt.ext)
			if ok != tt.ok {
				t.Fatalf("ParseFamily(%q) ok = %v, want %v", tt.ext, ok, tt.ok)
			}
			if !tt.ok {
				return
			}
			if family.Base != tt.base || family.V2 != tt.v2 {
				t.Errorf("ParseFamily(%q) family = %c v2=%v, want %c v2=%v",
					tt.ext, family.Base, family.V2, tt.base, tt.v2)
			}
			if number != tt.number {
				t.Errorf("ParseFamily(%q) number = %d, want %d", tt.ext, number, tt.number)
			}
		})
	}
}

func TestFamilyNumber(t *testing.T) {
	e := Family{Base: 'E'}
	l := Family{Base: 'L'}
	ex := Family{Base: 'E', V2: true}

	tests := []struct {
		name   string
		family Family
		ext    string
		number uint32
		ok     bool
	}{
		{name: "numeric", family: e, ext: ".E07", number: 7, ok: true},
		{name: "letters", family: e, ext: ".EAA", number: 100, ok: true},
		{name: "mixed case member", family: e, ext: ".e42", number: 42, ok: true},

		// Within a known family the leading letter may carry past Z.
		{name: "carry", family: e, ext: ".FAA", number: 776, ok: true},
		{name: "carry end", family: e, ext: ".FZZ", number: 1451, ok: true},

		// A different family's names are never members, however well formed.
		{name: "other family", family: e, ext: ".L02", ok: false},
		{name: "v1 in v2 set", family: ex, ext: ".E02", ok: false},
		{name: "v2 in v1 set", family: e, ext: ".Ex02", ok: false},
		{name: "before base", family: l, ext: ".E02", ok: false},

		// The numeric range exists only at the base letter: .F01 is a
		// different set's first segment, not this set's.
		{name: "numeric after carry", family: e, ext: ".F01", ok: false},

		{name: "sidecar", family: e, ext: ".md5", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			number, ok := tt.family.Number(tt.ext)
			if ok != tt.ok {
				t.Fatalf("Family(%c).Number(%q) ok = %v, want %v", tt.family.Base, tt.ext, ok, tt.ok)
			}
			if ok && number != tt.number {
				t.Errorf("Family(%c).Number(%q) = %d, want %d", tt.family.Base, tt.ext, number, tt.number)
			}
		})
	}
}

func TestExtRoundTrip(t *testing.T) {
	families := []Family{
		{Base: 'E'},
		{Base: 'L'},
		{Base: 's'},
		{Base: 'e'},
		{Base: 'E', V2: true},
	}

	for _, family := range families {
		t.Run(family.String(), func(t *testing.T) {
			for number := uint32(1); number <= 775; number++ {
				ext, err := family.Ext(number)
				if err != nil {
					t.Fatalf("Ext(%d) error = %v", number, err)
				}
				got, ok := family.Number(ext)
				if !ok {
					t.Fatalf("Ext(%d) = %q, which does not parse back", number, ext)
				}
				if got != number {
					t.Fatalf("Ext(%d) = %q, parsed back as %d", number, ext, got)
				}
			}
		})
	}
}

func TestExtSpelling(t *testing.T) {
	tests := []struct {
		family Family
		number uint32
		want   string
	}{
		{family: Family{Base: 'E'}, number: 1, want: ".E01"},
		{family: Family{Base: 'E'}, number: 99, want: ".E99"},
		{family: Family{Base: 'E'}, number: 100, want: ".EAA"},
		{family: Family{Base: 'E'}, number: 775, want: ".EZZ"},
		{family: Family{Base: 'E'}, number: 776, want: ".FAA"},
		{family: Family{Base: 'e'}, number: 100, want: ".eaa"},
		{family: Family{Base: 'L'}, number: 2, want: ".L02"},
		{family: Family{Base: 's'}, number: 3, want: ".s03"},
		{family: Family{Base: 'E', V2: true}, number: 1, want: ".Ex01"},
		{family: Family{Base: 'L', V2: true}, number: 12, want: ".Lx12"},
	}

	for _, tt := range tests {
		got, err := tt.family.Ext(tt.number)
		if err != nil {
			t.Errorf("Family(%c).Ext(%d) error = %v", tt.family.Base, tt.number, err)
			continue
		}
		if got != tt.want {
			t.Errorf("Family(%c).Ext(%d) = %q, want %q", tt.family.Base, tt.number, got, tt.want)
		}
	}
}

func TestExtBeyondNamingRange(t *testing.T) {
	// A Z-family set has no letter left to carry into.
	if _, err := (Family{Base: 'Z'}).Ext(776); err == nil {
		t.Error("Ext(776) on a .Z set succeeded, want an error")
	}
	if _, err := (Family{Base: 'E'}).Ext(0); err == nil {
		t.Error("Ext(0) succeeded, want an error: segment numbering starts at 1")
	}
	if _, err := (Family{}).Ext(1); err == nil {
		t.Error("Ext on the zero Family succeeded, want an error")
	}
}

// TestOrderingIsNumericNotLexical pins the rule the whole package exists for:
// .E99 precedes .EAA, and a mixed-case set still orders correctly. Sorting the
// extensions as strings would put .e02 last.
func TestOrderingIsNumericNotLexical(t *testing.T) {
	family := Family{Base: 'E'}
	shuffled := []string{".EAB", ".E99", ".e02", ".EAA", ".E01"}
	want := []string{".E01", ".e02", ".E99", ".EAA", ".EAB"}

	sort.Slice(shuffled, func(i, j int) bool {
		left, ok := family.Number(shuffled[i])
		if !ok {
			t.Fatalf("%q does not parse", shuffled[i])
		}
		right, ok := family.Number(shuffled[j])
		if !ok {
			t.Fatalf("%q does not parse", shuffled[j])
		}
		return left < right
	})

	if strings.Join(shuffled, " ") != strings.Join(want, " ") {
		t.Errorf("ordered %v, want %v", shuffled, want)
	}
}

// touch creates an empty file. Discovery is a filesystem operation and never
// reads a byte of any segment, so empty files exercise it exactly.
func touch(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	return path
}

func names(paths []string) []string {
	out := make([]string, len(paths))
	for i, path := range paths {
		out[i] = filepath.Base(path)
	}
	return out
}

func assertSet(t *testing.T, set *Set, want ...string) {
	t.Helper()
	got := names(set.Paths())
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("discovered %v, want %v", got, want)
	}
}

func TestDiscoverSingleSegment(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01")
	if missing := set.Missing(); len(missing) != 0 {
		t.Errorf("Missing() = %v, want none", missing)
	}
}

func TestDiscoverTwoSegments(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E02")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E02")
	if missing := set.Missing(); len(missing) != 0 {
		t.Errorf("Missing() = %v, want none", missing)
	}
}

// TestDiscoverFromLaterSegment checks that the set is enumerated from segment
// 1 whichever member names it, so an image opened by its .E03 still decodes
// from the beginning rather than from the middle.
func TestDiscoverFromLaterSegment(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E02")
	third := touch(t, dir, "disk.E03")

	set, err := Discover(third)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E02", "disk.E03")
}

func TestDiscoverGap(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E04")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}

	missing := set.Missing()
	if len(missing) != 2 || missing[0] != 2 || missing[1] != 3 {
		t.Fatalf("Missing() = %v, want [2 3]", missing)
	}

	expected := set.Names(missing)
	if strings.Join(expected, " ") != "disk.E02 disk.E03" {
		t.Errorf("Names(%v) = %v, want [disk.E02 disk.E03]", missing, expected)
	}
}

// TestDiscoverIgnoresUnrelatedFiles covers the files that really do sit
// alongside evidence: sidecar digests, logs, and other images whose names
// merely start the same way.
func TestDiscoverIgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E02")
	touch(t, dir, "disk.E01.md5")
	touch(t, dir, "disk.txt")
	touch(t, dir, "disk.json")
	touch(t, dir, "disk")
	touch(t, dir, "disk-2.E01")
	touch(t, dir, "other.E01")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E02")
}

// TestDiscoverIgnoresOtherFamilies checks that a directory holding both the
// physical and the logical acquisition of one evidence item does not merge
// them into a single set.
func TestDiscoverIgnoresOtherFamilies(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E02")
	touch(t, dir, "disk.L01")
	touch(t, dir, "disk.Ex01")
	touch(t, dir, "disk.s01")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E02")

	v2, err := Discover(filepath.Join(dir, "disk.Ex01"))
	if err != nil {
		t.Fatalf("Discover(.Ex01) error = %v", err)
	}
	assertSet(t, v2, "disk.Ex01")
}

// TestDiscoverSaturationRule is the sidecar trap: ".log" is a well-formed
// letter-pair extension of the L family, and would otherwise be read as
// segment 470 of a logical image.
func TestDiscoverSaturationRule(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.L01")
	touch(t, dir, "disk.log")
	touch(t, dir, "disk.lst")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.L01")
	if missing := set.Missing(); len(missing) != 0 {
		t.Fatalf("Missing() = %v, want none: a sidecar was counted as a segment", missing)
	}
}

// TestDiscoverAdmitsLetterPairsWhenSaturated is the other half of that rule:
// once all 99 numeric segments exist, segment 100 is real and must be found.
func TestDiscoverAdmitsLetterPairsWhenSaturated(t *testing.T) {
	dir := t.TempDir()
	family := Family{Base: 'E'}

	var first string
	for number := uint32(1); number <= 99; number++ {
		ext, err := family.Ext(number)
		if err != nil {
			t.Fatalf("Ext(%d) error = %v", number, err)
		}
		path := touch(t, dir, "disk"+ext)
		if number == 1 {
			first = path
		}
	}
	touch(t, dir, "disk.EAA")
	touch(t, dir, "disk.EAB")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(set.Paths()) != 101 {
		t.Fatalf("discovered %d segments, want 101", len(set.Paths()))
	}
	if got := filepath.Base(set.Paths()[98]); got != "disk.E99" {
		t.Errorf("segment 99 = %s, want disk.E99", got)
	}
	if got := filepath.Base(set.Paths()[99]); got != "disk.EAA" {
		t.Errorf("segment 100 = %s, want disk.EAA: .E99 must sort before .EAA", got)
	}
	if got := filepath.Base(set.Paths()[100]); got != "disk.EAB" {
		t.Errorf("segment 101 = %s, want disk.EAB", got)
	}
}

func TestDiscoverCaseInsensitiveMembers(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "DISK.e02")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "DISK.e02")
}

// TestDiscoverAmbiguousSpelling covers a case-sensitive filesystem holding two
// files that both claim to be segment 2. Guessing which is evidence is not
// this library's call to make.
func TestDiscoverAmbiguousSpelling(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	touch(t, dir, "disk.E02")
	touch(t, dir, "disk.e02")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	if len(entries) != 3 {
		t.Skipf("filesystem folds case (%d entries for 3 names); the ambiguity cannot arise here", len(entries))
	}

	if _, err := Discover(first); !errors.Is(err, ewferr.ErrCorruptImage) {
		t.Fatalf("Discover() error = %v, want ErrCorruptImage", err)
	}
}

func TestDiscoverNonSegmentExtension(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "disk.E01")
	raw := touch(t, dir, "disk.raw")

	set, err := Discover(raw)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.raw")
	if missing := set.Missing(); len(missing) != 0 {
		t.Errorf("Missing() = %v, want none for a file that names no set", missing)
	}
}

// TestDiscoverRendersEverySegment pins what the parallel accessors promise:
// every member appears in both renderings, including the member of a set whose
// name carries no number. Found by fuzzing, where a file named "0" produced a
// path with no number beside it.
func TestDiscoverRendersEverySegment(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"0", "disk.raw", "disk"} {
		t.Run(name, func(t *testing.T) {
			set, err := Discover(touch(t, dir, name))
			if err != nil {
				t.Fatalf("Discover() error = %v", err)
			}
			if len(set.Segments) != 1 {
				t.Fatalf("discovered %d segments, want 1", len(set.Segments))
			}
			if len(set.Paths()) != len(set.Segments) || len(set.Numbers()) != len(set.Segments) {
				t.Fatalf("%d segments render as %d paths and %d numbers",
					len(set.Segments), len(set.Paths()), len(set.Numbers()))
			}
			if set.Segments[0].Number != 0 {
				t.Errorf("segment number = %d, want 0: the name implies no position",
					set.Segments[0].Number)
			}
			if missing := set.Missing(); len(missing) != 0 {
				t.Errorf("Missing() = %v, want none for a name that implies no set", missing)
			}
		})
	}
}

func TestDiscoverMissingPath(t *testing.T) {
	dir := t.TempDir()

	_, err := Discover(filepath.Join(dir, "absent.E01"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Discover() error = %v, want fs.ErrNotExist", err)
	}
}

func TestDiscoverDirectory(t *testing.T) {
	dir := t.TempDir()

	if _, err := Discover(dir); err == nil {
		t.Fatal("Discover() on a directory succeeded, want an error")
	}
}

// TestDiscoverSkipsDirectoriesNamedLikeSegments checks that a directory
// occupying a segment's name is reported as a gap rather than opened.
func TestDiscoverSkipsDirectoriesNamedLikeSegments(t *testing.T) {
	dir := t.TempDir()
	first := touch(t, dir, "disk.E01")
	if err := os.Mkdir(filepath.Join(dir, "disk.E02"), 0o755); err != nil {
		t.Fatalf("creating directory: %v", err)
	}
	touch(t, dir, "disk.E03")

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E03")
	if missing := set.Missing(); len(missing) != 1 || missing[0] != 2 {
		t.Errorf("Missing() = %v, want [2]", missing)
	}
}

func TestDiscoverFollowsSymlinkedSegments(t *testing.T) {
	dir := t.TempDir()
	source := t.TempDir()

	first := touch(t, dir, "disk.E01")
	target := touch(t, source, "second")
	link := filepath.Join(dir, "disk.E02")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("creating a symlink needs privilege on this platform")
		}
		t.Fatalf("creating symlink: %v", err)
	}

	set, err := Discover(first)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	assertSet(t, set, "disk.E01", "disk.E02")
}
