// Package segname decodes and generates the file names of an EWF segment set.
//
// Acquisition tools name the segments of one image with a shared stem and a
// counting extension: disk.E01, disk.E02, and so on. The counting is not
// decimal throughout. It runs .E01 to .E99, then continues in letter pairs
// .EAA to .EZZ, then carries into the leading letter (.FAA). Two consequences
// follow, and both are the reason this package exists:
//
//   - Segments cannot be ordered by comparing their extensions as strings.
//     Every name is mapped to a segment number and the numbers are compared,
//     which is what puts .E99 before .EAA and keeps mixed-case sets in order.
//
//   - A name that parses is not necessarily a segment. ".log", ".lst" and
//     ".zip" are all well-formed letter-pair extensions of the L and s
//     families, so admitting them on syntax alone would let a sidecar file be
//     read as evidence.
package segname

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aoiflux/libewf/ewferr"
)

const (
	// letterBase is the segment number the first letter-pair extension
	// denotes: .E01 to .E99 exhaust the numeric range, so .EAA is segment 100.
	letterBase = 100

	// lettersPerLeading is how many segments one leading letter spans, AA..ZZ.
	lettersPerLeading = 26 * 26

	// maxNumeric is the highest segment expressible in the two-digit form.
	maxNumeric = 99

	// v2Marker is the character EWF2 inserts after the base letter. It is
	// lowercase in every dialect that writes it — .Ex01, .Lx01 — regardless of
	// how the rest of the name is cased, so it is emitted literally rather than
	// following the case of the family.
	v2Marker = 'x'
)

// Family identifies the naming family of a segment set: the letter every
// extension in the set starts with, and whether the set uses the
// four-character EWF2 spelling.
type Family struct {
	// Base is the leading letter as it was observed on disk, so names
	// generated for the set keep the case the set is written in.
	Base byte

	// V2 marks the EWF2 spelling, which inserts an 'x' after the base letter:
	// .Ex01 rather than .E01.
	V2 bool
}

// Valid reports whether f names a family at all. The zero Family does not.
func (f Family) Valid() bool {
	switch lower(f.Base) {
	case 'e', 'l', 's':
		return true
	default:
		return false
	}
}

// String renders the family as the extension prefix it contributes.
func (f Family) String() string {
	if !f.Valid() {
		return "<none>"
	}
	if f.V2 {
		return string([]byte{'.', f.Base, v2Marker})
	}
	return string([]byte{'.', f.Base})
}

// ParseFamily identifies the family and segment number of a segment file
// extension, which must include its leading dot.
//
// The base letter must name one of the three EWF families: E for physical
// images, L for logical ones, s for SMART. A leading letter carried past .EZZ
// (.FAA and beyond) is deliberately not accepted here, because in isolation it
// identifies no family: .FAA is segment 776 of an E set, but only the set can
// say so. Callers name a set by one of its members, and no caller names one by
// its 776th.
func ParseFamily(ext string) (Family, uint32, bool) {
	family, field, ok := splitExt(ext)
	if !ok || !family.Valid() {
		return Family{}, 0, false
	}
	number, ok := fieldNumber(field, 0)
	if !ok {
		return Family{}, 0, false
	}
	return family, number, true
}

// Number returns the segment number ext denotes within family f, and whether
// ext names a segment of f at all.
//
// Unlike ParseFamily this does accept a carried leading letter, because here
// the family is known: within an E set, .FAA can only mean segment 776.
func (f Family) Number(ext string) (uint32, bool) {
	if !f.Valid() {
		return 0, false
	}
	other, field, ok := splitExt(ext)
	if !ok || other.V2 != f.V2 {
		return 0, false
	}
	carry := int(upper(other.Base)) - int(upper(f.Base))
	if carry < 0 {
		return 0, false
	}
	return fieldNumber(field, uint32(carry))
}

// Ext returns the extension, including its leading dot, that names segment n
// of family f.
func (f Family) Ext(n uint32) (string, error) {
	if !f.Valid() {
		return "", fmt.Errorf("segname: cannot name segment %d without a family", n)
	}
	if n == 0 {
		return "", fmt.Errorf("segname: segment 0 does not exist")
	}

	var b strings.Builder
	b.WriteByte('.')

	if n <= maxNumeric {
		b.WriteByte(f.Base)
		if f.V2 {
			b.WriteByte(v2Marker)
		}
		fmt.Fprintf(&b, "%02d", n)
		return b.String(), nil
	}

	offset := n - letterBase
	carry := offset / lettersPerLeading
	if int(upper(f.Base))+int(carry) > 'Z' {
		return "", fmt.Errorf("segname: segment %d is beyond the naming range of a %s set", n, f)
	}
	rest := offset % lettersPerLeading

	// Past .EZZ the leading letter itself advances, so it replaces the base
	// letter rather than following it. The letter pair takes the case of the
	// family, so a set written as .e01 continues .eaa rather than changing
	// case partway through.
	alpha := byte('A')
	if !isUpper(f.Base) {
		alpha = 'a'
	}
	b.WriteByte(f.Base + byte(carry))
	if f.V2 {
		b.WriteByte(v2Marker)
	}
	b.WriteByte(alpha + byte(rest/26))
	b.WriteByte(alpha + byte(rest%26))
	return b.String(), nil
}

// splitExt breaks an extension into its family and its two-character counting
// field. It validates shape only: whether the family letter names an EWF
// family, and whether the field counts to something sensible, is decided by
// the caller, which knows whether a family is already established.
func splitExt(ext string) (Family, string, bool) {
	if len(ext) < 4 || len(ext) > 5 || ext[0] != '.' {
		return Family{}, "", false
	}
	if !isLetter(ext[1]) {
		return Family{}, "", false
	}
	family := Family{Base: ext[1]}
	field := ext[2:]
	if len(ext) == 5 {
		if lower(ext[2]) != 'x' {
			return Family{}, "", false
		}
		family.V2 = true
		field = ext[3:]
	}
	return family, field, true
}

// fieldNumber decodes a two-character counting field. carry is how far the
// leading letter has advanced past the family's base letter, each step of
// which spans a full AA..ZZ range.
func fieldNumber(field string, carry uint32) (uint32, bool) {
	if len(field) != 2 {
		return 0, false
	}

	if isDigit(field[0]) && isDigit(field[1]) {
		// The numeric range exists only at the base letter: .F01 is not
		// segment 1 of anything, it is a different set.
		if carry != 0 {
			return 0, false
		}
		n := uint32(field[0]-'0')*10 + uint32(field[1]-'0')
		if n == 0 {
			return 0, false
		}
		return n, true
	}

	if isLetter(field[0]) && isLetter(field[1]) {
		return letterBase +
			carry*lettersPerLeading +
			26*uint32(upper(field[0])-'A') +
			uint32(upper(field[1])-'A'), true
	}

	return 0, false
}

// Set is a segment set discovered on disk, ordered by segment number.
type Set struct {
	// Family is the naming family shared by every member, or the zero Family
	// when the path named no family and the set is the one file given.
	Family Family

	// Dir is the directory the set lives in.
	Dir string

	// Stem is the file name every member shares, without its extension.
	Stem string

	// Paths lists the segment files, ascending by segment number.
	Paths []string

	// Numbers lists the segment numbers, parallel to Paths.
	Numbers []uint32
}

// Discover finds every segment that belongs with path and returns them in
// segment order.
//
// path may name any numbered member of the set, not only the first: the set is
// identified by the stem and family of the name and then enumerated from
// segment 1, so opening an image by its .E03 finds the .E01 too. A path whose
// extension names no EWF family — a raw image, a name with no extension —
// yields a set of that one file, which is the honest reading of a name that
// says nothing about a set.
//
// Discovery reads the directory once rather than probing names in sequence.
// Probing stops at the first name that is absent, and so can never tell a
// complete set from one with a hole in it — which is the case that most needs
// reporting.
func Discover(path string) (*Set, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("segname: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("segname: %s is a directory, not a segment file: %w",
			path, ewferr.ErrUnsupportedFormat)
	}

	dir := filepath.Dir(path)
	name := filepath.Base(path)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	family, self, ok := ParseFamily(ext)
	if !ok {
		return &Set{Dir: dir, Stem: stem, Paths: []string{path}}, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("segname: unable to read directory %s: %w", dir, err)
	}

	found := make(map[uint32]string, len(entries))
	for _, entry := range entries {
		candidate := entry.Name()
		candidateExt := filepath.Ext(candidate)
		// Stems are compared without regard to case so that a set written as
		// disk.E01 and DISK.E02 still reads as one set. Sidecars are excluded
		// here rather than by a deny list: disk.E01.md5 has the stem
		// "disk.E01", which is not "disk".
		if !strings.EqualFold(strings.TrimSuffix(candidate, candidateExt), stem) {
			continue
		}
		number, ok := family.Number(candidateExt)
		if !ok {
			continue
		}
		full := filepath.Join(dir, candidate)
		if !isRegular(entry, full) {
			continue
		}
		if previous, clash := found[number]; clash {
			return nil, fmt.Errorf("segname: %s and %s both name segment %d of the set: %w",
				previous, candidate, number, ewferr.ErrCorruptImage)
		}
		found[number] = candidate
	}

	// A letter-pair extension names segment 100 or beyond, which cannot exist
	// unless all 99 numeric segments do. Enforcing that is not pedantry: .log,
	// .lst and .zip are all well-formed letter-pair extensions, and a sidecar
	// must never be counted as evidence.
	if !saturated(found) {
		for number := range found {
			if number >= letterBase {
				delete(found, number)
			}
		}
	}

	// The stat above proves path exists, and the caller's spelling of the file
	// it named is authoritative even if the directory scan reported another.
	found[self] = name

	numbers := make([]uint32, 0, len(found))
	for number := range found {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })

	set := &Set{Family: family, Dir: dir, Stem: stem, Numbers: numbers}
	set.Paths = make([]string, 0, len(numbers))
	for _, number := range numbers {
		set.Paths = append(set.Paths, filepath.Join(dir, found[number]))
	}
	return set, nil
}

// Missing returns the segment numbers absent between 1 and the highest number
// found, ascending.
//
// It reports holes only. A set that stops short of its true end is
// indistinguishable on disk from a complete one — nothing in a directory says
// how many segments the acquisition wrote — so that case is left to the
// reader, which requires the highest segment to carry a "done" section.
func (s *Set) Missing() []uint32 {
	if len(s.Numbers) == 0 {
		return nil
	}

	present := make(map[uint32]struct{}, len(s.Numbers))
	for _, number := range s.Numbers {
		present[number] = struct{}{}
	}

	var missing []uint32
	for number := uint32(1); number <= s.Numbers[len(s.Numbers)-1]; number++ {
		if _, ok := present[number]; !ok {
			missing = append(missing, number)
		}
	}
	return missing
}

// Names returns the file names the given segment numbers would carry in this
// set, so a caller can report which files to go and find. Numbers the family
// cannot express are skipped rather than reported as a wrong name.
func (s *Set) Names(numbers []uint32) []string {
	names := make([]string, 0, len(numbers))
	for _, number := range numbers {
		ext, err := s.Family.Ext(number)
		if err != nil {
			continue
		}
		names = append(names, s.Stem+ext)
	}
	return names
}

// saturated reports whether every numeric segment 1..99 is present.
func saturated(found map[uint32]string) bool {
	for number := uint32(1); number <= maxNumeric; number++ {
		if _, ok := found[number]; !ok {
			return false
		}
	}
	return true
}

// isRegular reports whether the entry is a regular file, following symlinks.
// Evidence directories are sometimes assembled from links to read-only media,
// and refusing to follow them would report a segment that is plainly there as
// missing. Directories are excluded, so a directory named like a segment
// surfaces as a gap rather than as an open failure.
func isRegular(entry fs.DirEntry, path string) bool {
	if entry.Type().IsRegular() {
		return true
	}
	if entry.Type()&fs.ModeSymlink == 0 {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDigit(c byte) bool  { return c >= '0' && c <= '9' }
func isUpper(c byte) bool  { return c >= 'A' && c <= 'Z' }
func isLetter(c byte) bool { return isUpper(c) || (c >= 'a' && c <= 'z') }

func lower(c byte) byte {
	if isUpper(c) {
		return c + ('a' - 'A')
	}
	return c
}

func upper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - ('a' - 'A')
	}
	return c
}
