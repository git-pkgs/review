package review

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/roles"
)

const (
	maxNames        = 6
	maxQuoted       = 3
	maxRoleEntries  = 8
	focusedDocsSize = 10
)

// changeSummary describes a group of code changes in sentences built from
// the facts gathered for each file.
func changeSummary(files []*File) []string {
	var out []string
	var added, deleted, moved []string
	for _, f := range files {
		switch f.Status {
		case Added:
			added = append(added, f.Path)
		case Deleted:
			deleted = append(deleted, f.Path)
		case Renamed, Copied:
			moved = append(moved, f.OldPath+" to "+f.Path)
		}
	}
	if len(added) > 0 {
		out = append(out, "Adds "+nameList(added)+".")
	}
	if len(deleted) > 0 {
		out = append(out, "Deletes "+nameList(deleted)+".")
	}
	if len(moved) > 0 {
		out = append(out, "Moves "+nameList(moved)+".")
	}
	out = append(out, modeSentences(files)...)
	out = append(out, declarationSentences(files)...)

	tests, sources := 0, 0
	for _, f := range files {
		switch {
		case has(f, roles.Test):
			tests++
		case has(f, roles.Source) || f.Surface != nil:
			sources++
		}
	}
	switch {
	case tests > 0 && tests == len(files):
		out = append(out, "Changes tests only.")
	case tests > 0:
		out = append(out, "Includes "+plural(tests, "test file")+".")
	case sources > 0:
		out = append(out, "No test files changed.")
	}
	return out
}

const (
	modeExecutable = "100755"
	modeRegular    = "100644"
)

// modeSentences describes permission and file type changes, which have no
// lines to show.
func modeSentences(files []*File) []string {
	var executable, plain, link, unlink, other []string
	for _, f := range files {
		if f.OldMode == "" || f.NewMode == "" || f.OldMode == f.NewMode {
			continue
		}
		switch {
		case f.NewMode == symlinkMode:
			link = append(link, f.Path)
		case f.OldMode == symlinkMode:
			unlink = append(unlink, f.Path)
		case f.OldMode == modeRegular && f.NewMode == modeExecutable:
			executable = append(executable, f.Path)
		case f.OldMode == modeExecutable && f.NewMode == modeRegular:
			plain = append(plain, f.Path)
		default:
			other = append(other, fmt.Sprintf("%s from %s to %s", f.Path, f.OldMode, f.NewMode))
		}
	}
	var out []string
	if len(executable) > 0 {
		out = append(out, "Makes "+nameList(executable)+" executable.")
	}
	if len(plain) > 0 {
		out = append(out, "Makes "+nameList(plain)+" no longer executable.")
	}
	if len(link) > 0 {
		out = append(out, "Replaces "+nameList(link)+" with a symbolic link.")
	}
	if len(unlink) > 0 {
		out = append(out, "Replaces the symbolic link "+nameList(unlink)+" with a file.")
	}
	if len(other) > 0 {
		out = append(out, "Changes the mode of "+nameList(other)+".")
	}
	return out
}

// declarations collects what a group's non-test files did to declarations.
type declarations struct {
	added, signature, body               []SymbolChange
	declared, changed, removed, contexts []string
}

func (d *declarations) add(f *File) {
	if has(f, roles.Test) {
		return
	}
	n, c, r := surfaceDecls(f)
	// Symbols describe head, so removals only show in the outline comparison.
	d.removed = append(d.removed, r...)
	if f.Symbols != nil {
		for _, s := range f.Symbols {
			switch s.Change {
			case SymbolAdded:
				d.added = append(d.added, s)
			case SymbolSignature:
				d.signature = append(d.signature, s)
			case SymbolBody:
				d.body = append(d.body, s)
			}
		}
		return
	}
	d.declared = append(d.declared, n...)
	d.changed = append(d.changed, c...)
	if (f.Surface == nil || f.Surface.BodyOnly()) && outline.Supported(f.Path) {
		for _, ctx := range f.Contexts {
			if !hasPrefix(ctx, importPrefixes) {
				d.contexts = append(d.contexts, cleanDecl(ctx))
			}
		}
	}
}

func declarationSentences(files []*File) []string {
	var d declarations
	for _, f := range files {
		d.add(f)
	}
	var out []string
	for _, s := range []struct {
		lead string
		syms []SymbolChange
	}{
		{"Adds ", d.added},
		{"Changes the declaration of ", d.signature},
		{"Changes the body of ", d.body},
	} {
		if len(s.syms) > 0 {
			out = append(out, s.lead+symbolList(s.syms)+".")
		}
	}
	for _, s := range []struct {
		lead  string
		items []string
	}{
		{"Declares ", d.declared},
		{"Changes ", d.changed},
		{"Removes ", d.removed},
		{"Edits inside ", d.contexts},
	} {
		if len(s.items) > 0 {
			out = append(out, s.lead+quotedList(dedupe(s.items))+".")
		}
	}
	return out
}

// surfaceDecls reads top-level declarations out of an outline comparison:
// new ones, ones whose line changed, and ones that went away.
func surfaceDecls(f *File) (added, changed, removed []string) {
	if f.Surface == nil {
		return nil, nil, nil
	}
	var minus, plus []string
	flush := func() {
		n := min(len(minus), len(plus))
		changed = append(changed, plus[:n]...)
		removed = append(removed, minus[n:]...)
		added = append(added, plus[n:]...)
		minus, plus = nil, nil
	}
	for _, l := range f.Surface.Lines {
		if l.Context != "" || hasPrefix(l.Text, commentPrefixes) || hasPrefix(l.Text, importPrefixes) {
			continue
		}
		if l.Op == "-" {
			if len(plus) > 0 {
				flush()
			}
			minus = append(minus, cleanDecl(l.Text))
		} else {
			plus = append(plus, cleanDecl(l.Text))
		}
	}
	flush()
	return added, changed, removed
}

func cleanDecl(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "{")
	s = strings.TrimSuffix(s, ":")
	return strings.TrimSpace(s)
}

var kindWords = map[string][2]string{
	kindFunc: {"function", "functions"},
	"method": {"method", "methods"},
	"type":   {"type", "types"},
	"class":  {"class", "classes"},
	"const":  {"constant", "constants"},
	"var":    {"variable", "variables"},
	"module": {"module", "modules"},
}

// symbolList names symbols by kind, exported ones first:
// "types A and B, and function c".
func symbolList(syms []SymbolChange) string {
	syms = slices.Clone(syms)
	slices.SortStableFunc(syms, func(a, b SymbolChange) int {
		if a.Exported != b.Exported {
			if a.Exported {
				return -1
			}
			return 1
		}
		return 0
	})
	extra := 0
	if len(syms) > maxNames {
		extra = len(syms) - maxNames
		syms = syms[:maxNames]
	}
	var kinds []string
	byKind := map[string][]string{}
	for _, s := range syms {
		if _, ok := byKind[s.Kind]; !ok {
			kinds = append(kinds, s.Kind)
		}
		if name := "`" + s.Name + "`"; !slices.Contains(byKind[s.Kind], name) {
			byKind[s.Kind] = append(byKind[s.Kind], name)
		}
	}
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names := byKind[k]
		word := k
		if w, ok := kindWords[k]; ok {
			word = w[0]
			if len(names) > 1 {
				word = w[1]
			}
		}
		parts = append(parts, word+" "+joinAnd(names))
	}
	s := strings.Join(parts, ", ")
	if len(parts) > 1 {
		s = strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
	if extra > 0 {
		s += fmt.Sprintf(" (and %d more)", extra)
	}
	return s
}

func quotedList(items []string) string {
	quoted := make([]string, 0, maxQuoted)
	for i, it := range items {
		if i == maxQuoted {
			break
		}
		quoted = append(quoted, "`"+it+"`")
	}
	s := joinAnd(quoted)
	if len(items) > maxQuoted {
		s += fmt.Sprintf(" (and %d more)", len(items)-maxQuoted)
	}
	return s
}

// docsSummary points at documentation changed by the same focused commits.
func (g *grouper) docsSummary(files, docs []*File) []string {
	in := map[string]bool{}
	for _, f := range files {
		in[f.Path] = true
	}
	isDoc := map[string]bool{}
	for _, d := range docs {
		isDoc[d.Path] = true
	}
	var found []string
	for _, c := range g.o.Commits {
		if len(c.Files) > focusedDocsSize || !slices.ContainsFunc(c.Files, func(cf CommitFile) bool { return in[cf.Path] }) {
			continue
		}
		for _, cf := range c.Files {
			if isDoc[cf.Path] && !slices.Contains(found, cf.Path) {
				found = append(found, cf.Path)
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	return []string{"Documented in " + nameList(found) + "."}
}

func roleSummary(files []*File) []string {
	entries := make([]string, 0, len(files))
	for _, f := range files {
		e := f.Path
		var notes []string
		if f.Status != Modified {
			notes = append(notes, f.Status)
		}
		if f.Dependency != nil {
			notes = append(notes, f.Dependency.Ecosystem+" "+f.Dependency.Kind)
		}
		if isMarkup(f.Path) || outline.Supported(f.Path) {
			for _, c := range f.Contexts {
				notes = append(notes, truncate(c, maxContextWidth))
			}
		}
		if len(notes) > 0 {
			e += " (" + strings.Join(notes, ", ") + ")"
		}
		entries = append(entries, e)
	}
	if len(entries) > maxRoleEntries {
		n := len(entries) - maxRoleEntries
		entries = append(entries[:maxRoleEntries], fmt.Sprintf("%d more", n))
	}
	return []string{"Changes " + strings.Join(entries, "; ") + "."}
}

func mechanicalSummary(reason string, files []*File) []string {
	var names []string
	for _, f := range files {
		if f.OldPath != "" {
			names = append(names, f.OldPath+" to "+f.Path)
		} else {
			names = append(names, f.Path)
		}
	}
	switch {
	case strings.HasPrefix(reason, Renamed), strings.HasPrefix(reason, Copied):
		return []string{"Contents are identical: " + nameList(names) + "."}
	case reason == "whitespace only":
		return []string{"Only indentation or spacing changes in " + nameList(names) + "."}
	}
	return []string{nameList(names) + "."}
}

func isMarkup(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown", ".mdx", ".rst", ".adoc", ".org":
		return true
	}
	return false
}

func nameList(names []string) string {
	if len(names) > maxNames {
		return strings.Join(names[:maxNames], ", ") + fmt.Sprintf(" and %d more", len(names)-maxNames)
	}
	return joinAnd(names)
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func upperFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[size:]
}

func dedupe(items []string) []string {
	var out []string
	for _, it := range items {
		if it != "" && !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return out
}
