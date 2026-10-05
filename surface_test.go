package review

import (
	"slices"
	"strings"
	"testing"
)

func TestDiffSurfaceUnsupported(t *testing.T) {
	if s := diffSurface("notes.txt", []byte("a"), []byte("b")); s != nil {
		t.Errorf("surface for unsupported file = %+v", s)
	}
}

func TestDiffSurfaceMemberContext(t *testing.T) {
	base := []byte("package p\n\ntype T struct {\n\tA int\n}\n")
	head := []byte("package p\n\ntype T struct {\n\tA int\n\tB string\n}\n")
	s := diffSurface("t.go", base, head)
	want := []SurfaceLine{{Op: "+", Text: "B string", Context: "type T struct {"}}
	if s == nil || !slices.Equal(s.Lines, want) {
		t.Errorf("surface = %+v, want %+v", s, want)
	}
}

func TestDiffSurfaceTooLarge(t *testing.T) {
	var b strings.Builder
	b.WriteString("package p\n")
	for i := range 2100 {
		b.WriteString("func F")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("() {}\n")
	}
	src := []byte(b.String())
	s := diffSurface("big.go", src, append([]byte(nil), src...))
	if s == nil || !s.Truncated || s.BodyOnly() {
		t.Errorf("surface = %+v, want truncated", s)
	}
}
