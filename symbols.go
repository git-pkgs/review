package review

import (
	"slices"
	"unicode/utf8"
)

// kindFunc is the symbol kind for functions and methods.
const kindFunc = "func"

// maxSignature is the length at which outline cuts signatures short.
const maxSignature = 240

// symbolChanges maps the diff onto the head declarations the caller supplied.
// A declaration whose body changed only because something nested in it
// changed, such as a class around an edited method, is left out, as are
// additions nested inside another addition or inside a changed function,
// such as a local variable.
func symbolChanges(d FileDiff) []SymbolChange {
	if len(d.Symbols) == 0 || d.Status == Deleted {
		return nil
	}
	lines := changedLines(d.Hunks)
	changes := make([]string, len(d.Symbols))
	for i, s := range d.Symbols {
		switch {
		case d.Status == Added:
			changes[i] = SymbolAdded
		case !lines.any(s.StartLine, s.EndLine):
		case d.BaseSymbols != nil && wholeInSignature(s) && inBase(d, s, true):
			// Unchanged apart from spacing or comments, as when gofmt
			// realigns a block.
		case d.BaseSymbols != nil && inBase(d, s, true):
			changes[i] = SymbolBody
		case lines.all(s.StartLine, s.EndLine) && (d.BaseSymbols == nil || !inBase(d, s, false)):
			changes[i] = SymbolAdded
		case lines.added[s.StartLine] || lines.all(s.StartLine, s.EndLine):
			changes[i] = SymbolSignature
		default:
			changes[i] = SymbolBody
		}
	}
	folded := slices.Clone(changes)
	for i := range d.Symbols {
		if changes[i] == SymbolAdded && insideChanged(d.Symbols, changes, i) {
			folded[i] = ""
		}
	}
	changes = folded
	var out []SymbolChange
	for i, s := range d.Symbols {
		if changes[i] == "" || (changes[i] == SymbolBody && containsChanged(d.Symbols, changes, i)) {
			continue
		}
		out = append(out, SymbolChange{Name: s.Name, Kind: s.Kind, Exported: s.Exported, Change: changes[i]})
	}
	return out
}

// inBase reports whether base declares s: with sameSignature, one with the
// same qualified name, kind and signature; otherwise as many of that name
// and kind as head does, so s is not an extra one.
func inBase(d FileDiff, s Symbol, sameSignature bool) bool {
	same := func(o Symbol) bool { return o.Name == s.Name && o.Qualified == s.Qualified && o.Kind == s.Kind }
	if sameSignature {
		// Truncation backs up to a UTF-8 boundary, so a cut can be shorter
		// than the byte limit. Equal prefixes don't establish equality.
		return s.Signature != "" && len(s.Signature) <= maxSignature-utf8.UTFMax &&
			slices.ContainsFunc(d.BaseSymbols, func(o Symbol) bool { return same(o) && o.Signature == s.Signature })
	}
	count := func(syms []Symbol) int {
		n := 0
		for _, o := range syms {
			if same(o) {
				n++
			}
		}
		return n
	}
	return count(d.BaseSymbols) >= count(d.Symbols)
}

// wholeInSignature reports whether s's signature holds all of it: a one-line
// constant or variable, whose signature includes its value.
func wholeInSignature(s Symbol) bool {
	return (s.Kind == "const" || s.Kind == "var") && s.StartLine == s.EndLine
}

func containsChanged(syms []Symbol, changes []string, i int) bool {
	outer := syms[i]
	for j, s := range syms {
		if j != i && (changes[j] == SymbolAdded || (changes[j] != "" && s.Kind == kindFunc)) && s.StartLine >= outer.StartLine && s.EndLine <= outer.EndLine &&
			(s.StartLine != outer.StartLine || s.EndLine != outer.EndLine) {
			return true
		}
	}
	return false
}

func insideChanged(syms []Symbol, changes []string, i int) bool {
	inner := syms[i]
	for j, s := range syms {
		if j != i && (changes[j] == SymbolAdded || (changes[j] != "" && s.Kind == kindFunc)) && s.StartLine <= inner.StartLine && s.EndLine >= inner.EndLine &&
			(s.StartLine != inner.StartLine || s.EndLine != inner.EndLine) {
			return true
		}
	}
	return false
}
