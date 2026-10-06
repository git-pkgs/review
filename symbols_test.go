package review

import (
	"strings"
	"testing"
)

func TestSymbolChangesAgainstBase(t *testing.T) {
	value := Symbol{Name: "Value", Kind: kindFunc, Signature: "func Value() int", StartLine: 3, EndLine: 3}
	replaced := []Hunk{{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 1, Lines: []string{"-func Value() int { return 1 }", "+func Value() int { return 2 }"}}}
	fooA := Symbol{Name: "Foo", Kind: kindFunc, Signature: "func (A) Foo() int", StartLine: 3, EndLine: 3}
	fooB := Symbol{Name: "Foo", Kind: kindFunc, Signature: "func (B) Foo() int", StartLine: 4, EndLine: 4}
	inserted := []Hunk{{OldStart: 3, OldLines: 0, NewStart: 4, NewLines: 1, Lines: []string{"+func (B) Foo() int { return 2 }"}}}

	key := Symbol{Name: "Key", Kind: "const", Signature: `Key = "sha256"`, StartLine: 3, EndLine: 3}
	realigned := []Hunk{{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 2, Lines: []string{"-\tKey = \"sha256\"", "+\tKey      = \"sha256\"", "+\tFilename = \"files.txt\""}}}
	long := key
	long.Signature = "Key = " + strings.Repeat("x", maxSignature)

	tests := []struct {
		name   string
		d      FileDiff
		change string
	}{
		{"one-line body edit", FileDiff{Symbols: []Symbol{value}, BaseSymbols: []Symbol{value}, Hunks: replaced}, SymbolBody},
		{"one-line body edit without base", FileDiff{Symbols: []Symbol{value}, Hunks: replaced}, SymbolAdded},
		{"one-line signature edit", FileDiff{
			Symbols:     []Symbol{{Name: "Value", Kind: kindFunc, Signature: "func Value() int64", StartLine: 3, EndLine: 3}},
			BaseSymbols: []Symbol{value}, Hunks: replaced,
		}, SymbolSignature},
		{"renamed into a new name", FileDiff{
			Symbols:     []Symbol{{Name: "Amount", Kind: kindFunc, Signature: "func Amount() int", StartLine: 3, EndLine: 3}},
			BaseSymbols: []Symbol{value}, Hunks: replaced,
		}, SymbolAdded},
		{"realigned constant", FileDiff{Symbols: []Symbol{key}, BaseSymbols: []Symbol{key}, Hunks: realigned}, ""},
		{"constant too long to compare", FileDiff{Symbols: []Symbol{long}, BaseSymbols: []Symbol{long}, Hunks: realigned}, SymbolSignature},
		{"method on another receiver", FileDiff{Symbols: []Symbol{fooA, fooB}, BaseSymbols: []Symbol{fooA}, Hunks: inserted}, SymbolAdded},
	}
	for _, tt := range tests {
		tt.d.Path, tt.d.Status = "a.go", Modified
		f, err := Analyze(tt.d)
		if err != nil {
			t.Fatal(err)
		}
		if tt.change == "" {
			if len(f.Symbols) != 0 {
				t.Errorf("%s: symbols = %+v, want none", tt.name, f.Symbols)
			}
			continue
		}
		if len(f.Symbols) != 1 || f.Symbols[0].Change != tt.change {
			t.Errorf("%s: symbols = %+v, want one %s", tt.name, f.Symbols, tt.change)
		}
	}
}
