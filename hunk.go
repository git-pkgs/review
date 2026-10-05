package review

import (
	"bytes"
	"fmt"
	"path"
	"strconv"
	"strings"
	"unicode"
)

// Hunk is one hunk of a file's diff. Lines keep their leading ' ', '-' or
// '+', and a "\ No newline at end of file" marker stays after the line it
// applies to.
type Hunk struct {
	Header   string   `json:"header"`
	OldStart int      `json:"old_start"`
	OldLines int      `json:"old_lines"`
	NewStart int      `json:"new_start"`
	NewLines int      `json:"new_lines"`
	Lines    []string `json:"-"`
}

// ParseHunkHeader reads the line ranges from "@@ -a,b +c,d @@ context".
func ParseHunkHeader(header string) (Hunk, error) {
	h := Hunk{Header: header}
	fields := strings.Fields(header)
	if len(fields) < 3 || fields[0] != "@@" || !strings.HasPrefix(fields[1], "-") || !strings.HasPrefix(fields[2], "+") {
		return h, fmt.Errorf("malformed hunk header %q", header)
	}
	var err error
	if h.OldStart, h.OldLines, err = parseRange(fields[1][1:]); err != nil {
		return h, fmt.Errorf("malformed hunk header %q", header)
	}
	if h.NewStart, h.NewLines, err = parseRange(fields[2][1:]); err != nil {
		return h, fmt.Errorf("malformed hunk header %q", header)
	}
	return h, nil
}

func parseRange(r string) (int, int, error) {
	start, count, ok := strings.Cut(r, ",")
	s, err := strconv.Atoi(start)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return s, 1, nil
	}
	c, err := strconv.Atoi(count)
	return s, c, err
}

// lineChanges records which head lines a diff added, and the head lines next
// to which it removed something.
type lineChanges struct {
	added   map[int]bool
	touched map[int]bool
}

func changedLines(hunks []Hunk) lineChanges {
	c := lineChanges{added: map[int]bool{}, touched: map[int]bool{}}
	for _, h := range hunks {
		line := h.NewStart
		if h.NewLines == 0 {
			// A pure deletion's NewStart is the line before the gap.
			c.touched[max(line, 1)] = true
			c.touched[line+1] = true
		}
		for _, l := range h.Lines {
			if l == "" {
				line++
				continue
			}
			switch l[0] {
			case '+':
				c.added[line] = true
				c.touched[line] = true
				line++
			case '-':
				c.touched[line] = true
			case '\\':
			default:
				line++
			}
		}
		if len(h.Lines) == 0 {
			for i := range h.NewLines {
				c.touched[h.NewStart+i] = true
			}
		}
	}
	return c
}

// any reports whether a change touches lines start through end.
func (c lineChanges) any(start, end int) bool {
	for l := start; l <= end; l++ {
		if c.touched[l] {
			return true
		}
	}
	return false
}

// all reports whether every line from start through end was added.
func (c lineChanges) all(start, end int) bool {
	for l := start; l <= end; l++ {
		if !c.added[l] {
			return false
		}
	}
	return end >= start
}

// whitespaceOnly reports whether a change differs only in whitespace. With
// both full versions it compares them whole; otherwise it compares each
// hunk's old and new text, context included, in order, so reordered or moved
// lines don't count. Where indentation is syntax, only trailing whitespace
// and blank lines may differ.
func whitespaceOnly(d FileDiff) bool {
	norm := normalizer(d.Path)
	base, okBase := side(d.Base, false)
	head, okHead := side(d.Head, false)
	if okBase && okHead {
		return !bytes.Equal(base, head) && norm(string(base)) == norm(string(head))
	}
	if len(d.Hunks) == 0 {
		return false
	}
	for _, h := range d.Hunks {
		if len(h.Lines) == 0 {
			return false
		}
		var before, after strings.Builder
		for _, l := range h.Lines {
			if l == "" {
				l = " "
			}
			switch l[0] {
			case '\\':
			case '-':
				before.WriteString(l[1:] + "\n")
			case '+':
				after.WriteString(l[1:] + "\n")
			default:
				before.WriteString(l[1:] + "\n")
				after.WriteString(l[1:] + "\n")
			}
		}
		if norm(before.String()) != norm(after.String()) {
			return false
		}
	}
	return true
}

// indentSyntax lists file types whose indentation carries meaning.
var indentSyntax = map[string]bool{
	".py": true, ".pyi": true, ".yaml": true, ".yml": true, ".haml": true, ".pug": true, ".jade": true,
	".coffee": true, ".nim": true, ".sass": true, ".styl": true, ".slim": true, ".mk": true,
	".hs": true, ".elm": true, ".fs": true, ".fsx": true,
	"Makefile": true, "makefile": true, "GNUmakefile": true,
}

// normalizer returns how text is compared for a whitespace-only change: line
// by line with blank lines removed, each line reduced to its words, or for
// indentation-sensitive files with only trailing space removed. Line breaks
// stay significant, since joining a line onto a comment changes the code, and
// splitting on whitespace keeps "a b" and "ab" apart.
func normalizer(name string) func(string) string {
	line := func(l string) string { return strings.Join(strings.Fields(l), " ") }
	if indentSyntax[path.Ext(name)] || indentSyntax[path.Base(name)] {
		line = func(l string) string { return strings.TrimRightFunc(l, unicode.IsSpace) }
	}
	return func(s string) string {
		var lines []string
		for l := range strings.SplitSeq(s, "\n") {
			if l = line(l); strings.TrimSpace(l) != "" {
				lines = append(lines, l)
			}
		}
		return strings.Join(lines, "\n")
	}
}

// editFingerprint is the removed and added text of a diff without context,
// so two files that received the same edit share a fingerprint.
func editFingerprint(hunks []Hunk) string {
	var b strings.Builder
	for _, h := range hunks {
		if len(h.Lines) == 0 {
			return ""
		}
		for _, l := range h.Lines {
			if l != "" && (l[0] == '-' || l[0] == '+') {
				b.WriteString(l)
				b.WriteByte('\n')
			}
		}
		b.WriteByte(0)
	}
	return b.String()
}

// literalFingerprint is editFingerprint with string and number values
// replaced by placeholders, so translations of the same new keys match. A
// string followed by a colon is a key and is kept, and so is a string that
// runs code: one with an interpolation or an f, s or $ prefix.
func literalFingerprint(hunks []Hunk) string {
	var b strings.Builder
	for _, h := range hunks {
		if len(h.Lines) == 0 {
			return ""
		}
		for _, l := range h.Lines {
			if l != "" && (l[0] == '-' || l[0] == '+') {
				b.WriteString(l[:1] + maskLiterals(l[1:]))
				b.WriteByte('\n')
			}
		}
		b.WriteByte(0)
	}
	return b.String()
}

func maskLiterals(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			end := closingDelim(s, i)
			lit := s[i : end+1]
			isKey := strings.HasPrefix(strings.TrimLeft(s[end+1:], " \t"), ":")
			if isKey || interpolates(lit) || (i > 0 && strings.IndexByte("fFsS$", s[i-1]) >= 0) {
				b.WriteString(lit)
			} else {
				b.WriteString(`""`)
			}
			i = end
		case c >= '0' && c <= '9' && (i == 0 || !isWordByte(s[i-1])):
			for i+1 < len(s) && (isWordByte(s[i+1]) || s[i+1] == '.') {
				i++
			}
			b.WriteByte('0')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// interpolates reports whether a string literal embeds expressions, as in
// JavaScript `${x}`, Ruby "#{x}", Swift "\(x)", or shell and Kotlin "$(x)",
// "${x}" and "$x".
func interpolates(lit string) bool {
	for _, marker := range []string{"${", "#{", "\\(", "$("} {
		if strings.Contains(lit, marker) {
			return true
		}
	}
	for i := 0; i+1 < len(lit); i++ {
		if lit[i] == '$' && (isWordByte(lit[i+1]) && (lit[i+1] < '0' || lit[i+1] > '9')) {
			return true
		}
	}
	return false
}

// closingDelim finds the index of the quote closing the one at start, or the
// last index when it isn't closed on this line.
func closingDelim(s string, start int) int {
	for i := start + 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case s[start]:
			return i
		}
	}
	return len(s) - 1
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
