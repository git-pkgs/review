package gitdiff

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
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

// contextFiles are files outline reads to resolve imports between changed
// files in other directories: a Go module's path, and Python packages that
// can re-export a module's names.
var contextFiles = map[string]bool{"go.mod": true, "__init__.py": true}

// graph builds an outline graph of the changed files at rev, written out of
// the committed blobs so the working tree never affects it. Links only join
// changed files, so the rest of the tree is left out apart from
// contextFiles: parsing all of it is slow, and under that load outline's
// parse timeout drops files.
func (g gitRunner) graph(ctx context.Context, rev string, changed []string) (*outline.Graph, error) {
	tmp, err := os.MkdirTemp("", "review-head-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := g.extract(ctx, rev, tmp, graphFiles(changed)); err != nil {
		return nil, err
	}
	return outline.Build(tmp, outline.Options{})
}

// graphFiles selects the changed paths and the contextFiles in their
// directories and every parent directory.
func graphFiles(changed []string) func(string) bool {
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, p := range changed {
		files[p] = true
		for d := path.Dir(p); !dirs[d]; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	return func(name string) bool {
		return files[name] || (contextFiles[path.Base(name)] && dirs[path.Dir(name)])
	}
}

// extract writes the regular files of rev that keep selects into dir exactly
// as stored: no checkout filters, line-ending conversion or export attributes
// apply, and paths are from the repository root wherever git runs. Symlinks
// and submodules are left out.
func (g gitRunner) extract(ctx context.Context, rev, dir string, keep func(string) bool) error {
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
	err = writeTree(string(listing), blobs, root, keep)
	return errors.Join(err, root.Close(), blobs.close())
}

// writeTree writes each regular blob that keep selects from an ls-tree -z
// listing, whose entries are "<mode> <type> <oid>\t<path>".
func writeTree(listing string, blobs *blobReader, root *os.Root, keep func(string) bool) error {
	const dirPerm = 0o755
	for entry := range strings.SplitSeq(listing, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != modeRegular && fields[0] != modeExecutable) || !keep(name) {
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

// addGraphs fills in each changed file's declarations and the links between
// files from a graph of the head commit, then each modified file's base
// declarations from a graph of the base commit. A graph that can't be built
// leaves a warning rather than an error.
func (g gitRunner) addGraphs(ctx context.Context, o *review.Overview, rng review.Range, diffs []review.FileDiff) {
	byPath := make(map[string]*review.FileDiff, len(diffs))
	changed := make([]string, 0, len(diffs))
	for i := range diffs {
		byPath[diffs[i].Path] = &diffs[i]
		changed = append(changed, diffs[i].Path)
	}
	head, err := g.graph(ctx, rng.HeadRev, changed)
	if err != nil {
		o.Warnings = append(o.Warnings, "no code graph, so files are grouped without links: "+err.Error())
		return
	}
	for _, w := range head.Warnings {
		o.Warnings = append(o.Warnings, "code graph: "+w)
	}
	var rewritten []int
	var old []string
	for i := range diffs {
		d := &diffs[i]
		if d.Head != nil && d.Head.Complete {
			d.Symbols = fileSymbols(head, d.Path, d.Head.Data)
		}
		if d.Symbols != nil && d.Status != review.Added && d.Base != nil && d.Base.Complete {
			rewritten = append(rewritten, i)
			old = append(old, basePath(d))
		}
	}
	o.Links = links(head, byPath)
	if len(old) == 0 {
		return
	}
	base, err := g.graph(ctx, rng.BaseRev, old)
	if err != nil {
		o.Warnings = append(o.Warnings, "no base code graph, so rewritten declarations may read as added: "+err.Error())
		return
	}
	for _, w := range base.Warnings {
		o.Warnings = append(o.Warnings, "base code graph: "+w)
	}
	for _, i := range rewritten {
		d := &diffs[i]
		// Empty rather than nil: a base without declarations was still read.
		d.BaseSymbols = append([]review.Symbol{}, fileSymbols(base, basePath(d), d.Base.Data)...)
	}
}

func basePath(d *review.FileDiff) string {
	if d.OldPath != "" {
		return d.OldPath
	}
	return d.Path
}

// fileSymbols returns the declarations graph g records for path, with line
// ranges computed from the file's contents in that version.
func fileSymbols(g *outline.Graph, path string, src []byte) []review.Symbol {
	var syms []review.Symbol
	for _, n := range g.Nodes {
		if n.File != path || !symbolKinds[n.Kind] || n.Line == 0 {
			continue
		}
		syms = append(syms, review.Symbol{
			Name:      n.Name,
			Qualified: n.Qualified,
			Kind:      n.Kind,
			Signature: n.Sig,
			StartLine: n.Line,
			EndLine:   max(n.Line, lineAt(src, n.End)),
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
