package review

import "slices"

// kindFunc is the symbol kind for functions and methods.
const kindFunc = "func"

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
		case d.Status == Added || lines.all(s.StartLine, s.EndLine):
			changes[i] = SymbolAdded
		case lines.added[s.StartLine]:
			changes[i] = SymbolSignature
		case lines.any(s.StartLine, s.EndLine):
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
