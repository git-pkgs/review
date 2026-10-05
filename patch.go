package review

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

const (
	gitlinkMode = "160000"
	symlinkMode = "120000"
	devNull     = "/dev/null"

	readBufferSize = 64 * 1024
	// plainHeaderLines is the "---" and "+++" pair that starts a file in a
	// diff without a "diff --git" line.
	plainHeaderLines = 2
	// permDigits is the trailing permission part of a git mode such as 100644.
	permDigits = 3
)

// FromPatch analyses and groups every file in a unified diff. A file that
// appears more than once, as in a git format-patch series, is reported once
// with its changes combined; one created and deleted within the series is
// left out, since it exists at neither end.
func FromPatch(r io.Reader) (*Overview, error) {
	var diffs []FileDiff
	seen := map[string]int{}
	commits, err := ReadPatch(r, func(d FileDiff) error {
		if i, ok := seen[d.Path]; ok {
			diffs[i] = mergeDiffs(diffs[i], d)
			return nil
		}
		seen[d.Path] = len(diffs)
		diffs = append(diffs, d)
		return nil
	})
	if err != nil {
		return nil, err
	}
	o := &Overview{Files: []File{}, Commits: commits}
	for _, d := range diffs {
		if d.Status == "" {
			continue
		}
		f, err := Analyze(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Path, err)
		}
		o.Add(f)
	}
	o.Group()
	return o, nil
}

// mergeDiffs combines two diffs of one path from successive patches. The
// combined status follows whether the file existed before the first patch and
// after the second, the old mode comes from the first and the new mode from
// the second, and contents are kept only for a file the second patch added
// whole. A file that exists at neither end gets an empty status.
func mergeDiffs(a, b FileDiff) FileDiff {
	before := a.Status != Added && a.Status != ""
	after := b.Status != Deleted
	m := a
	m.Additions += b.Additions
	m.Deletions += b.Deletions
	m.Binary = a.Binary || b.Binary
	m.Hunks = append(slices.Clone(a.Hunks), b.Hunks...)
	m.NewMode = b.NewMode
	m.Symlink = b.Symlink
	m.Submodule = b.Submodule
	m.Base, m.Head = nil, nil
	switch {
	case !before && after:
		m.Status = Added
		if b.Status == Added {
			m.Head = b.Head
		}
	case before && !after:
		m.Status, m.NewMode = Deleted, ""
	case before && after:
		if a.Status != Renamed && a.Status != Copied {
			m.Status = Modified
		}
	default:
		m.Status = ""
	}
	return m
}

// ReadPatch parses a unified diff as written by git diff, git format-patch or
// diff -u, calling visit once per file, and returns the commits described by
// any format-patch headers. Added and deleted files carry their full
// contents, since the diff holds every line; other files carry none.
func ReadPatch(r io.Reader, visit func(FileDiff) error) ([]Commit, error) {
	return readPatch(r, false, func(d FileDiff, _ string) error { return visit(d) })
}

// ReadPatchSections is ReadPatch that also passes each file's section of the
// patch exactly as written, for showing it again unchanged.
func ReadPatchSections(r io.Reader, visit func(d FileDiff, section string) error) ([]Commit, error) {
	return readPatch(r, true, visit)
}

func readPatch(r io.Reader, capture bool, visit func(FileDiff, string) error) ([]Commit, error) {
	p := &patchReader{r: bufio.NewReaderSize(r, readBufferSize), capture: capture}
	for {
		d, err := p.next()
		if errors.Is(err, io.EOF) {
			return p.commits, nil
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", p.line, err)
		}
		if n := len(p.commits); n > 0 {
			p.commits[n-1].Files = append(p.commits[n-1].Files, CommitFile{Path: d.Path, Additions: d.Additions, Deletions: d.Deletions})
		}
		if err := visit(d, strings.Join(p.section, "")); err != nil {
			return nil, err
		}
		p.section = p.section[:0]
	}
}

type patchReader struct {
	r       *bufio.Reader
	line    int
	pending *rawLine

	// capture keeps the raw lines read since the current file began.
	capture bool
	section []string

	commits []Commit
	header  headerState
	body    []string
}

type headerState int

const (
	outsideHeader headerState = iota
	inHeaders
	inSubject
	inBody
)

// mailHeader follows the email headers git format-patch writes before each
// diff, collecting the commit's subject and message.
func (p *patchReader) mailHeader(line string) {
	if sha, ok := strings.CutPrefix(line, "From "); ok && isCommitLine(sha) {
		p.finishBody()
		p.commits = append(p.commits, Commit{SHA: sha[:40]})
		p.header = inHeaders
		return
	}
	if p.header == outsideHeader || len(p.commits) == 0 {
		return
	}
	c := &p.commits[len(p.commits)-1]
	switch p.header {
	case inHeaders, inSubject:
		switch {
		case line == "":
			p.header = inBody
		case p.header == inSubject && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")):
			c.Subject += " " + strings.TrimSpace(line)
		case strings.HasPrefix(line, "Subject: "):
			c.Subject = stripPatchTag(line[len("Subject: "):])
			p.header = inSubject
		default:
			p.header = inHeaders
		}
	case inBody:
		if line == "---" {
			p.finishBody()
			return
		}
		p.body = append(p.body, line)
	}
}

func (p *patchReader) finishBody() {
	if p.header == inBody && len(p.commits) > 0 {
		p.commits[len(p.commits)-1].Body = strings.TrimSpace(strings.Join(p.body, "\n"))
	}
	p.body = nil
	p.header = outsideHeader
}

func isCommitLine(rest string) bool {
	const shaLen = 40
	if len(rest) < shaLen {
		return false
	}
	for _, r := range rest[:shaLen] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// stripPatchTag removes a leading "[PATCH 2/3]" from a subject.
func stripPatchTag(s string) string {
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "] "); end > 0 {
			return s[end+2:]
		}
	}
	return s
}

type rawLine struct{ text, raw string }

func (p *patchReader) readLine() (string, error) {
	var l rawLine
	if p.pending != nil {
		l, p.pending = *p.pending, nil
	} else {
		raw, err := p.r.ReadString('\n')
		if raw == "" && err != nil {
			return "", err
		}
		p.line++
		l = rawLine{text: strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r"), raw: raw}
	}
	if p.capture {
		p.section = append(p.section, l.raw)
	}
	return l.text, nil
}

// unread pushes back the line just read, which belongs to what follows.
func (p *patchReader) unread(s string) {
	raw := s + "\n"
	if n := len(p.section); p.capture && n > 0 {
		raw = p.section[n-1]
		p.section = p.section[:n-1]
	}
	p.pending = &rawLine{text: s, raw: raw}
}

// startSection drops captured lines from before a file's first n lines.
func (p *patchReader) startSection(n int) {
	if p.capture && len(p.section) > n {
		p.section = append(p.section[:0], p.section[len(p.section)-n:]...)
	}
}

// next returns the following file, skipping anything between files such as
// commit messages and diffstats.
func (p *patchReader) next() (FileDiff, error) {
	for {
		line, err := p.readLine()
		if err != nil {
			return FileDiff{}, err
		}
		if rest, ok := strings.CutPrefix(line, "diff --git "); ok {
			p.finishBody()
			p.startSection(1)
			return p.gitFile(rest)
		}
		if p.header != outsideHeader || strings.HasPrefix(line, "From ") {
			p.mailHeader(line)
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			plus, err := p.readLine()
			if err != nil {
				return FileDiff{}, err
			}
			if strings.HasPrefix(plus, "+++ ") {
				p.startSection(plainHeaderLines)
				var f fileBuilder
				f.setPaths(line[4:], plus[4:])
				return p.hunks(&f)
			}
			p.unread(plus)
		}
	}
}

type fileBuilder struct {
	d        FileDiff
	oldPath  string
	newPath  string
	oldMode  string
	newMode  string
	rename   bool
	copy     bool
	isNew    bool
	isDelete bool
	base     strings.Builder
	head     strings.Builder

	hunkBytes int
}

func (f *fileBuilder) setPaths(minus, plus string) {
	f.oldPath = patchPath(minus)
	f.newPath = patchPath(plus)
	if f.oldPath == devNull {
		f.isNew = true
	}
	if f.newPath == devNull {
		f.isDelete = true
	}
}

// gitFile reads a git extended header and the hunks that follow it. The
// header ends at the first line that isn't one git writes, which for a file
// without hunks may be the next commit's email header.
func (p *patchReader) gitFile(header string) (FileDiff, error) {
	var f fileBuilder
	f.oldPath, f.newPath = splitGitHeader(header)
	for {
		line, err := p.readLine()
		if errors.Is(err, io.EOF) {
			return f.finish(), nil
		}
		if err != nil {
			return FileDiff{}, err
		}
		switch {
		case strings.HasPrefix(line, "diff --git "), strings.HasPrefix(line, "@@ "):
			p.unread(line)
			return p.hunks(&f)
		case strings.HasPrefix(line, "--- "):
			plus, err := p.readLine()
			if err != nil {
				return FileDiff{}, err
			}
			f.setPaths(line[4:], strings.TrimPrefix(plus, "+++ "))
			return p.hunks(&f)
		case strings.HasPrefix(line, "old mode "):
			f.oldMode = line[len("old mode "):]
		case strings.HasPrefix(line, "new mode "):
			f.newMode = line[len("new mode "):]
		case strings.HasPrefix(line, "new file mode "):
			f.isNew, f.newMode = true, line[len("new file mode "):]
		case strings.HasPrefix(line, "deleted file mode "):
			f.isDelete, f.oldMode = true, line[len("deleted file mode "):]
		case strings.HasPrefix(line, "index "):
			if _, mode, ok := strings.Cut(line[len("index "):], " "); ok {
				f.oldMode, f.newMode = mode, mode
			}
		case strings.HasPrefix(line, "similarity index "):
			f.d.Similarity, _ = strconv.Atoi(strings.TrimSuffix(line[len("similarity index "):], "%"))
		case strings.HasPrefix(line, "dissimilarity index "):
		case strings.HasPrefix(line, "rename from "):
			f.rename, f.oldPath = true, unquote(line[len("rename from "):])
		case strings.HasPrefix(line, "rename to "):
			f.rename, f.newPath = true, unquote(line[len("rename to "):])
		case strings.HasPrefix(line, "copy from "):
			f.copy, f.oldPath = true, unquote(line[len("copy from "):])
		case strings.HasPrefix(line, "copy to "):
			f.copy, f.newPath = true, unquote(line[len("copy to "):])
		case strings.HasPrefix(line, "Binary files "):
			f.d.Binary = true
		case line == "GIT binary patch":
			f.d.Binary = true
			p.skipBinaryPatch()
		default:
			p.unread(line)
			return f.finish(), nil
		}
	}
}

// skipBinaryPatch consumes the literal or delta blocks after "GIT binary
// patch": a "literal N" or "delta N" line, base85 lines, and a blank line,
// once for each direction.
func (p *patchReader) skipBinaryPatch() {
	for {
		line, err := p.readLine()
		if err != nil {
			return
		}
		if line != "" && !strings.HasPrefix(line, "literal ") && !strings.HasPrefix(line, "delta ") && !isBase85Line(line) {
			p.unread(line)
			return
		}
	}
}

// isBase85Line reports whether line is one line of git's binary patch data:
// a length letter (A-Z for 1-26 bytes, a-z for 27-52) and five characters
// for every four bytes.
func isBase85Line(line string) bool {
	const groupBytes, groupChars, lowerBase = 4, 5, 27
	if len(line) < 1+groupChars {
		return false
	}
	var n int
	switch c := line[0]; {
	case c >= 'A' && c <= 'Z':
		n = int(c-'A') + 1
	case c >= 'a' && c <= 'z':
		n = int(c-'a') + lowerBase
	default:
		return false
	}
	return len(line)-1 == (n+groupBytes-1)/groupBytes*groupChars
}

// hunks reads hunks until the next file header. Hunk bodies are consumed by
// their line counts, so body lines that look like headers are not misread.
func (p *patchReader) hunks(f *fileBuilder) (FileDiff, error) {
	for {
		line, err := p.readLine()
		if errors.Is(err, io.EOF) {
			return f.finish(), nil
		}
		if err != nil {
			return FileDiff{}, err
		}
		if !strings.HasPrefix(line, "@@ ") {
			if strings.HasPrefix(line, "Binary files ") {
				f.d.Binary = true
				continue
			}
			if line == "GIT binary patch" {
				f.d.Binary = true
				p.skipBinaryPatch()
				continue
			}
			p.unread(line)
			return f.finish(), nil
		}
		h, err := ParseHunkHeader(line)
		if err != nil {
			return FileDiff{}, err
		}
		f.d.Hunks = append(f.d.Hunks, h)
		if err := p.hunkBody(f, &f.d.Hunks[len(f.d.Hunks)-1]); err != nil {
			return FileDiff{}, err
		}
	}
}

func (p *patchReader) hunkBody(f *fileBuilder, h *Hunk) error {
	oldLines, newLines := h.OldLines, h.NewLines
	var last *strings.Builder
	for oldLines > 0 || newLines > 0 {
		line, err := p.readLine()
		if errors.Is(err, io.EOF) {
			return errors.New("hunk ends early")
		}
		if err != nil {
			return err
		}
		if line == "" {
			line = " "
		}
		f.keepLine(h, line)
		switch line[0] {
		case ' ':
			oldLines--
			newLines--
			last = nil
		case '-':
			oldLines--
			f.d.Deletions++
			last = f.keep(&f.base, f.isDelete, line[1:])
		case '+':
			newLines--
			f.d.Additions++
			last = f.keep(&f.head, f.isNew, line[1:])
		case '\\':
			trimNewline(last)
		default:
			return fmt.Errorf("unexpected hunk line %q", line)
		}
		if oldLines < 0 || newLines < 0 {
			return errors.New("hunk longer than its header")
		}
	}
	if line, err := p.readLine(); err == nil {
		if strings.HasPrefix(line, "\\") {
			f.keepLine(h, line)
			trimNewline(last)
		} else {
			p.unread(line)
		}
	}
	return nil
}

// keepLine stores a hunk line, "\ No newline" markers included, until the
// file's hunks reach MaxContentBytes, after which the file keeps no hunk
// lines at all.
func (f *fileBuilder) keepLine(h *Hunk, line string) {
	if f.hunkBytes > MaxContentBytes {
		return
	}
	f.hunkBytes += len(line)
	h.Lines = append(h.Lines, line)
}

// keep records a line of an added or deleted file's contents, up to
// MaxContentBytes, and returns the builder written to.
func (f *fileBuilder) keep(b *strings.Builder, whole bool, text string) *strings.Builder {
	if !whole || b.Len() > MaxContentBytes {
		return nil
	}
	b.WriteString(text)
	b.WriteByte('\n')
	return b
}

func trimNewline(b *strings.Builder) {
	if b == nil || b.Len() == 0 {
		return
	}
	s := b.String()
	b.Reset()
	b.WriteString(strings.TrimSuffix(s, "\n"))
}

func (f *fileBuilder) finish() FileDiff {
	d := f.d
	if f.hunkBytes > MaxContentBytes {
		for i := range d.Hunks {
			d.Hunks[i].Lines = nil
		}
	}
	d.Path, d.OldPath = f.newPath, ""
	switch {
	case f.isNew:
		d.Status = Added
		d.Head = wholeContent(&f.head, d.Binary)
	case f.isDelete:
		d.Status, d.Path = Deleted, f.oldPath
		d.Base = wholeContent(&f.base, d.Binary)
	case f.rename:
		d.Status, d.OldPath = Renamed, f.oldPath
	case f.copy:
		d.Status, d.OldPath = Copied, f.oldPath
	case f.oldMode != "" && f.newMode != "" && fileType(f.oldMode) != fileType(f.newMode):
		d.Status = TypeChanged
	default:
		d.Status = Modified
	}
	d.OldMode, d.NewMode = f.oldMode, f.newMode
	if f.isNew {
		d.OldMode = ""
	}
	if f.isDelete {
		d.NewMode = ""
	}
	d.Symlink = f.oldMode == symlinkMode || f.newMode == symlinkMode
	d.Submodule = f.oldMode == gitlinkMode || f.newMode == gitlinkMode
	return d
}

func wholeContent(b *strings.Builder, binary bool) *Content {
	if binary {
		return nil
	}
	data := []byte(b.String())
	if len(data) > MaxContentBytes {
		return &Content{Data: data[:MaxContentBytes]}
	}
	return &Content{Data: data, Complete: true}
}

func fileType(mode string) string {
	if len(mode) < permDigits {
		return mode
	}
	return mode[:len(mode)-permDigits]
}

// patchPath strips the a/ or b/ prefix from a ---/+++ path, along with any
// timestamp diff -u appends after a tab.
func patchPath(s string) string {
	if strings.HasPrefix(s, `"`) {
		return stripPrefix(unquote(s))
	}
	s, _, _ = strings.Cut(s, "\t")
	if s == devNull {
		return s
	}
	return stripPrefix(s)
}

func stripPrefix(s string) string {
	if strings.HasPrefix(s, "a/") || strings.HasPrefix(s, "b/") {
		return s[2:]
	}
	return s
}

// splitGitHeader recovers the two paths from "a/x b/x". Unquoted paths
// containing spaces are only unambiguous when both sides are equal, which
// holds for every header without a rename; renames also carry
// "rename from/to" lines that override this.
func splitGitHeader(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		if end := closingQuote(s); end > 0 {
			a, b := s[:end+1], strings.TrimSpace(s[end+1:])
			return stripPrefix(unquote(a)), stripPrefix(unquote(b))
		}
	}
	for i := range len(s) {
		if s[i] != ' ' {
			continue
		}
		if a, b := stripPrefix(s[:i]), stripPrefix(s[i+1:]); a == b {
			return a, b
		}
	}
	a, b, _ := strings.Cut(s, " ")
	return stripPrefix(a), stripPrefix(unquote(b))
}

func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// unquote decodes git's C-style quoting, which Go's double-quoted string
// syntax covers.
func unquote(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}
