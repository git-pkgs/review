package review

import (
	"slices"
	"testing"

	"github.com/git-pkgs/roles"
)

func headers(hs ...string) []Hunk {
	out := make([]Hunk, 0, len(hs))
	for _, h := range hs {
		out = append(out, Hunk{Header: h})
	}
	return out
}

func TestHunkContexts(t *testing.T) {
	got := hunkContexts(headers(
		"@@ -1 +1 @@ func A() {",
		"@@ -5 +5 @@",
		"@@ -9 +9 @@ func A() {",
		"@@ -12 +12 @@ func B() {",
		"@@ -20 +20 @@ func C() {",
		"@@ -30 +30 @@ func D() {",
	))
	want := []string{"func A() {", "func B() {", "func C() {"}
	if !slices.Equal(got, want) {
		t.Errorf("hunkContexts = %q, want %q", got, want)
	}
}

func TestSkipReason(t *testing.T) {
	for path, want := range map[string]string{
		"yarn.lock":   "generated (lockfile)",
		"go.sum":      "generated (lockfile supplement)",
		"go.mod":      "",
		"src/main.go": "",
	} {
		f, err := Analyze(FileDiff{Path: path, Status: Modified})
		if err != nil {
			t.Fatal(err)
		}
		if f.Skip != want {
			t.Errorf("%s skip = %q, want %q", path, f.Skip, want)
		}
	}
	src, err := roles.Classify("src/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := skipReason(src, nil, true); got != "binary" {
		t.Errorf("binary skip = %q", got)
	}
}

func TestAnalyzeDependency(t *testing.T) {
	f, err := Analyze(FileDiff{Path: "go.mod", Status: Modified})
	if err != nil {
		t.Fatal(err)
	}
	if f.Dependency == nil || f.Dependency.Ecosystem != "golang" || f.Dependency.Kind != "manifest" {
		t.Errorf("dependency = %+v", f.Dependency)
	}
}

func TestAnalyzeMechanical(t *testing.T) {
	ws := []Hunk{{Header: "@@ -1,2 +1,2 @@", OldStart: 1, OldLines: 2, NewStart: 1, NewLines: 2,
		Lines: []string{"-func A() {", "-\treturn 1", "+func A()  {", "+    return 1"}}}
	tests := []struct {
		d    FileDiff
		want string
	}{
		{FileDiff{Path: "a.go", Status: Renamed, OldPath: "b.go"}, "renamed without changes"},
		{FileDiff{Path: "a.go", Status: Renamed, OldPath: "b.go", Additions: 1}, ""},
		{FileDiff{Path: "a.go", Status: Modified, Hunks: ws, Additions: 2, Deletions: 2}, "whitespace only"},
		{FileDiff{Path: "a.go", Status: Modified, Hunks: headers("@@ -1 +1 @@"), Additions: 1, Deletions: 1}, ""},
	}
	for _, tt := range tests {
		f, err := Analyze(tt.d)
		if err != nil {
			t.Fatal(err)
		}
		if f.Mechanical != tt.want {
			t.Errorf("%+v mechanical = %q, want %q", tt.d, f.Mechanical, tt.want)
		}
	}
}

func TestAnalyzeSymbols(t *testing.T) {
	// Head lines: 1 package, 3-5 Parse (signature changed), 7-9 Serve (body
	// changed), 11-13 New (added), 15-17 Same (untouched).
	d := FileDiff{
		Path:   "p.go",
		Status: Modified,
		Hunks: []Hunk{
			{NewStart: 3, NewLines: 1, Lines: []string{"-func Parse() {", "+func Parse(strict bool) {"}},
			{NewStart: 8, NewLines: 1, Lines: []string{"-\treturn", "+\tprintln()"}},
			{NewStart: 11, NewLines: 3, Lines: []string{"+func New() {", "+\treturn", "+}"}},
		},
		Symbols: []Symbol{
			{Name: "Parse", Kind: "func", StartLine: 3, EndLine: 5, Exported: true},
			{Name: "Serve", Kind: "func", StartLine: 7, EndLine: 9, Exported: true},
			{Name: "New", Kind: "func", StartLine: 11, EndLine: 13, Exported: true},
			{Name: "Same", Kind: "func", StartLine: 15, EndLine: 17, Exported: true},
		},
	}
	f, err := Analyze(d)
	if err != nil {
		t.Fatal(err)
	}
	want := []SymbolChange{
		{Name: "Parse", Kind: "func", Exported: true, Change: SymbolSignature},
		{Name: "Serve", Kind: "func", Exported: true, Change: SymbolBody},
		{Name: "New", Kind: "func", Exported: true, Change: SymbolAdded},
	}
	if !slices.Equal(f.Symbols, want) {
		t.Errorf("symbols = %+v", f.Symbols)
	}
}

func TestAnalyzeLocalDeclaration(t *testing.T) {
	d := FileDiff{
		Path:   "a.go",
		Status: Modified,
		Hunks:  []Hunk{{NewStart: 4, NewLines: 1, Lines: []string{"+\tvar err error"}}},
		Symbols: []Symbol{
			{Name: "Run", Kind: "func", StartLine: 3, EndLine: 6},
			{Name: "err", Kind: "var", StartLine: 4, EndLine: 4},
		},
	}
	f, err := Analyze(d)
	if err != nil {
		t.Fatal(err)
	}
	if want := []SymbolChange{{Name: "Run", Kind: "func", Change: SymbolBody}}; !slices.Equal(f.Symbols, want) {
		t.Errorf("symbols = %+v", f.Symbols)
	}
}

func TestAnalyzeNestedSymbols(t *testing.T) {
	// A class at 1-10 holding an edited method at 3-5 and an added one at 7-9.
	d := FileDiff{
		Path:   "a.rb",
		Status: Modified,
		Hunks: []Hunk{
			{NewStart: 4, NewLines: 1, Lines: []string{"-  1", "+  2"}},
			{NewStart: 7, NewLines: 3, Lines: []string{"+  def b", "+    3", "+  end"}},
		},
		Symbols: []Symbol{
			{Name: "A", Kind: "class", StartLine: 1, EndLine: 10},
			{Name: "a", Kind: "func", StartLine: 3, EndLine: 5},
			{Name: "b", Kind: "func", StartLine: 7, EndLine: 9},
		},
	}
	f, err := Analyze(d)
	if err != nil {
		t.Fatal(err)
	}
	want := []SymbolChange{{Name: "a", Kind: "func", Change: SymbolBody}, {Name: "b", Kind: "func", Change: SymbolAdded}}
	if !slices.Equal(f.Symbols, want) {
		t.Errorf("symbols = %+v", f.Symbols)
	}
}
