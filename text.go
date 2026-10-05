package review

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const (
	maxSurfaceLines = 20
	maxContextWidth = 60
	shortRevLen     = 12
	shortSHALen     = 7
	maxCommitLines  = 10
	wrapWidth       = 88
)

var statusLetters = map[string]string{
	Added:       "A",
	Modified:    "M",
	Deleted:     "D",
	Renamed:     "R",
	Copied:      "C",
	TypeChanged: "T",
}

var sectionTitles = []struct {
	kinds []string
	title string
}{
	{[]string{GroupChange}, "Read in this order"},
	{[]string{GroupDocs, GroupConfig, GroupDependencies}, "Also changed"},
	{[]string{GroupMechanical}, "Mechanical changes"},
	{[]string{GroupSkim}, "Safe to skim"},
}

// TextOptions control rendering.
type TextOptions struct {
	// Markdown writes GitHub-flavoured Markdown instead of terminal text.
	Markdown bool
	// Declarations adds each file's declaration-level changes under it.
	Declarations bool
}

// Text writes the overview as terminal text.
func (o *Overview) Text(w io.Writer) error { return o.Render(w, TextOptions{}) }

// Render writes the overview for a reader: what the change is, its groups in
// reading order, then what can be skimmed.
func (o *Overview) Render(w io.Writer, opts TextOptions) error {
	r := &renderer{w: w, md: opts.Markdown, decls: opts.Declarations, o: o}
	r.header()
	r.commits()
	for _, sec := range sectionTitles {
		var groups []Group
		for _, g := range o.Groups {
			if slices.Contains(sec.kinds, g.Kind) {
				groups = append(groups, g)
			}
		}
		if len(groups) == 0 {
			continue
		}
		r.heading(sec.title)
		for _, g := range groups {
			r.group(g)
		}
	}
	for _, warning := range o.Warnings {
		r.para("Warning: " + warning)
	}
	if !r.md && len(o.Groups) > 0 {
		r.para("review show <number> prints a group's diff. -v adds declaration changes per file, --json has everything.")
	}
	return r.err
}

type renderer struct {
	w     io.Writer
	md    bool
	decls bool
	o     *Overview
	err   error
}

func (r *renderer) printf(format string, args ...any) {
	if r.err == nil {
		_, r.err = fmt.Fprintf(r.w, format, args...)
	}
}

func (r *renderer) heading(title string) {
	if r.md {
		r.printf("\n### %s\n\n", title)
		return
	}
	r.printf("\n%s\n\n", title)
}

func (r *renderer) para(text string) {
	r.printf("\n%s\n", r.wrap(text, ""))
}

func (r *renderer) header() {
	o := r.o
	title := "Changes"
	switch {
	case len(o.Commits) == 1:
		title = o.Commits[0].Subject
	case o.Branch != "":
		title = "Branch " + o.Branch
	}
	if r.md {
		r.printf("## %s\n\n", title)
	} else {
		r.printf("%s\n", title)
	}
	var facts []string
	if rng := o.Range; rng != nil {
		sep := ".."
		if rng.MergeBase {
			sep = "..."
		}
		facts = append(facts, fmt.Sprintf("%s%s%s (%s..%s)", rng.Base, sep, rng.Head, short(rng.BaseRev), short(rng.HeadRev)))
	}
	if len(o.Commits) > 0 {
		facts = append(facts, plural(len(o.Commits), "commit"))
	}
	facts = append(facts, plural(len(o.Files), "file"), fmt.Sprintf("+%d -%d", o.Additions, o.Deletions))
	r.printf("%s\n", strings.Join(facts, ", "))
}

func (r *renderer) commits() {
	if len(r.o.Commits) <= 1 {
		return
	}
	r.heading("Commits")
	for i, c := range r.o.Commits {
		if i == maxCommitLines {
			r.printf("%sand %d more\n", r.bullet(), len(r.o.Commits)-i)
			break
		}
		sha := c.SHA
		if len(sha) > shortSHALen {
			sha = sha[:shortSHALen]
		}
		if r.md {
			r.printf("- %s `%s`\n", c.Subject, sha)
		} else {
			r.printf("  %s %s\n", sha, c.Subject)
		}
	}
}

func (r *renderer) bullet() string {
	if r.md {
		return "- "
	}
	return "  "
}

func (r *renderer) group(g Group) {
	r.groupAt(g, strconv.Itoa(g.ID)+".", "")
}

// groupAt writes a group numbered label, with its text indented by indent.
func (r *renderer) groupAt(g Group, label, indent string) {
	size := plural(len(g.Files), "file")
	if g.Additions+g.Deletions > 0 {
		size += fmt.Sprintf(", +%d -%d", g.Additions, g.Deletions)
	}
	if r.md {
		r.printf("**%s %s** (%s)\n\n", label, g.Title, size)
	} else {
		r.printf("%s%s %s  (%s)\n", indent, label, g.Title, size)
	}
	if len(g.Summary) > 0 {
		if r.md {
			r.printf("%s\n\n", strings.Join(g.Summary, " "))
		} else {
			r.printf("%s\n", r.wrap(strings.Join(g.Summary, " "), indent+"   "))
		}
	}
	if len(g.Children) > 0 {
		for _, c := range g.Children {
			if !r.md {
				r.printf("\n")
			}
			r.groupAt(c, fmt.Sprintf("%s%d", label, c.ID), indent+"   ")
		}
	} else if g.Kind == GroupChange || g.Kind == GroupMechanical {
		r.files(g, indent)
	}
	if !r.md && indent == "" {
		r.printf("\n")
	}
}

func (r *renderer) files(g Group, indent string) {
	byPath := make(map[string]File, len(r.o.Files))
	for _, f := range r.o.Files {
		byPath[f.Path] = f
	}
	width := 0
	for _, p := range g.Files {
		width = max(width, len(p))
	}
	for _, p := range g.Files {
		f := byPath[p]
		if r.md {
			r.printf("- `%s` %s %s\n", f.Path, statusLetter(f.Status), counts(f))
		} else {
			r.printf("%s     %s %-*s  %s\n", indent, statusLetter(f.Status), width, f.Path, counts(f))
		}
		if r.decls {
			r.declarations(f, indent)
		}
	}
	if r.md {
		r.printf("\n")
	}
}

func (r *renderer) declarations(f File, indent string) {
	pad := indent + "         "
	if r.md {
		pad = "  - "
	}
	s := f.Surface
	switch {
	case s == nil:
		return
	case s.Truncated:
		r.printf("%sdeclarations too large to compare\n", pad)
		return
	case s.BodyOnly():
		r.printf("%sdeclarations unchanged\n", pad)
		return
	}
	lines := s.Lines
	if f.Status == Added || f.Status == Deleted {
		lines = topLevel(lines)
	}
	for i, l := range lines {
		if i == maxSurfaceLines {
			r.printf("%s... %d more\n", pad, len(lines)-i)
			return
		}
		text := l.Text
		if l.Context != "" {
			text = cleanDecl(l.Context) + " > " + text
		}
		if r.md {
			r.printf("%s`%s %s`\n", pad, l.Op, text)
		} else {
			r.printf("%s%s %s\n", pad, l.Op, text)
		}
	}
}

// wrap breaks text into lines of at most wrapWidth, each starting with indent.
func (r *renderer) wrap(text, indent string) string {
	if r.md {
		return text
	}
	var b strings.Builder
	line := indent
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent) && len(line)+1+len(word) > wrapWidth {
			b.WriteString(line + "\n")
			line = indent
		}
		if len(line) > len(indent) {
			line += " "
		}
		line += word
	}
	b.WriteString(line)
	return b.String()
}

// topLevel keeps the declarations of a whole-file add or delete, dropping
// members, comments and imports that would otherwise dominate the listing.
func topLevel(lines []SurfaceLine) []SurfaceLine {
	var out []SurfaceLine
	for _, l := range lines {
		if l.Context == "" && !hasPrefix(l.Text, commentPrefixes) && !hasPrefix(l.Text, importPrefixes) {
			out = append(out, l)
		}
	}
	return out
}

var (
	commentPrefixes = []string{"//", "#", "/*", "*", "--", ";", "%"}
	importPrefixes  = []string{"import ", "import(", "from ", "package ", "use ", "require ", "require(", "require_relative ", "using ", "#include"}
)

func hasPrefix(text string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

func dirLabel(dir string) string {
	if dir == "." {
		return "./"
	}
	return dir + "/"
}

func statusLetter(status string) string {
	if l, ok := statusLetters[status]; ok {
		return l
	}
	return status
}

func counts(f File) string {
	if f.Binary {
		return skipBinary
	}
	return fmt.Sprintf("+%d -%d", f.Additions, f.Deletions)
}

func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

func short(rev string) string {
	if len(rev) > shortRevLen {
		return rev[:shortRevLen]
	}
	return rev
}
