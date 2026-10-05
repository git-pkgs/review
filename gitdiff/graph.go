package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/git-pkgs/outline"
	"github.com/git-pkgs/review"
)

// symbolKinds are the graph node kinds reported as declarations.
var symbolKinds = map[string]bool{
	outline.KindFunc:  true,
	outline.KindType:  true,
	outline.KindClass: true,
	outline.KindConst: true,
	outline.KindVar:   true,
}

// linkRelations are the edges that tie one file's code to another's.
var linkRelations = map[string]bool{
	outline.RelCalls:      true,
	outline.RelReferences: true,
	outline.RelInherits:   true,
	outline.RelEmbeds:     true,
	outline.RelImports:    true,
	outline.RelLoads:      true,
}

// headGraph builds an outline graph of the head tree from a copy written out
// of the committed blobs, so the working tree's state, its filters and any
// untracked files never affect the result.
func (g gitRunner) headGraph(ctx context.Context, headRev string) (*outline.Graph, error) {
	tmp, err := os.MkdirTemp("", "review-head-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := g.extract(ctx, headRev, tmp); err != nil {
		return nil, err
	}
	return outline.Build(tmp, outline.Options{})
}

// extract writes the regular files of rev into dir exactly as stored: no
// checkout filters, line-ending conversion or export attributes apply, and
// paths are from the repository root wherever git runs. Symlinks and
// submodules are left out.
func (g gitRunner) extract(ctx context.Context, rev, dir string) error {
	listing, err := g.run(ctx, "ls-tree", "-r", "-z", "--full-tree", rev)
	if err != nil {
		return err
	}
	blobs, err := g.blobs(ctx)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return errors.Join(err, blobs.close())
	}
	err = writeTree(string(listing), blobs, root)
	return errors.Join(err, root.Close(), blobs.close())
}

// writeTree writes each regular blob in an ls-tree -z listing, whose entries
// are "<mode> <type> <oid>\t<path>".
func writeTree(listing string, blobs *blobReader, root *os.Root) error {
	const dirPerm = 0o755
	for entry := range strings.SplitSeq(listing, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != modeRegular && fields[0] != modeExecutable) {
			continue
		}
		local := filepath.FromSlash(name)
		if err := root.MkdirAll(filepath.Dir(local), dirPerm); err != nil {
			return err
		}
		f, err := root.Create(local)
		if err != nil {
			return err
		}
		if err := errors.Join(blobs.copy(fields[2], f), f.Close()); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// fileSymbols returns the declarations graph g records for path, with line
// ranges computed from the file's head contents.
func fileSymbols(g *outline.Graph, path string, head []byte) []review.Symbol {
	var syms []review.Symbol
	for _, n := range g.Nodes {
		if n.File != path || !symbolKinds[n.Kind] || n.Line == 0 {
			continue
		}
		syms = append(syms, review.Symbol{
			Name:      n.Name,
			Kind:      n.Kind,
			StartLine: n.Line,
			EndLine:   max(n.Line, lineAt(head, n.End)),
			Exported:  n.Exported,
		})
	}
	return syms
}

// lineAt converts a byte offset to a one-based line number.
func lineAt(src []byte, offset int) int {
	if offset <= 0 {
		return 1
	}
	offset = min(offset, len(src))
	return bytes.Count(src[:offset-1], []byte{'\n'}) + 1
}

// links finds edges from changed lines in one changed file to changed
// declarations in another.
func links(g *outline.Graph, diffs map[string]*review.FileDiff) []review.Link {
	type span struct{ start, end int }
	nodeFile := map[string]string{}
	nodeRange := map[string]span{}
	for _, n := range g.Nodes {
		if n.File == "" {
			continue
		}
		nodeFile[n.ID] = n.File
		if d, ok := diffs[n.File]; ok && d.Head != nil {
			nodeRange[n.ID] = span{n.Line, max(n.Line, lineAt(d.Head.Data, n.End))}
		}
	}
	changed := map[string]lineSet{}
	for path, d := range diffs {
		changed[path] = touchedLines(d.Hunks)
	}
	seen := map[review.Link]bool{}
	var out []review.Link
	for _, e := range g.Edges {
		if !linkRelations[e.Rel] {
			continue
		}
		from, to := nodeFile[e.From], nodeFile[e.To]
		if from == "" || to == "" || from == to {
			continue
		}
		dFrom, okFrom := diffs[from]
		dTo, okTo := diffs[to]
		if !okFrom || !okTo {
			continue
		}
		if dFrom.Status != review.Added && !changed[from][e.Line] {
			continue
		}
		if dTo.Status != review.Added {
			r, ok := nodeRange[e.To]
			if !ok || !changed[to].overlaps(r.start, r.end) {
				continue
			}
		}
		l := review.Link{From: from, To: to}
		if !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

type lineSet map[int]bool

func (s lineSet) overlaps(start, end int) bool {
	for l := start; l <= end; l++ {
		if s[l] {
			return true
		}
	}
	return false
}

// touchedLines lists head lines a file's hunks added or removed next to.
func touchedLines(hunks []review.Hunk) lineSet {
	s := lineSet{}
	for _, h := range hunks {
		if h.NewLines == 0 {
			s[h.NewStart] = true
			s[h.NewStart+1] = true
		}
		for i := range h.NewLines {
			s[h.NewStart+i] = true
		}
	}
	return s
}
