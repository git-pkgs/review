// Package gitdiff reads a commit range from a local repository with the git
// binary and analyses it with the review package.
package gitdiff

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/git-pkgs/review"
)

const maxRevisions = 2

const headRef = "HEAD"

// Options select the commits to compare. An empty Base means the default
// branch, an empty Head means HEAD. MergeBase compares Head against the merge
// base of the two, as a pull request does.
type Options struct {
	Base      string
	Head      string
	MergeBase bool
}

// ParseRange turns command-line revisions into Options using git's range
// syntax: "a..b" compares a and b directly, while "a...b", "a b" and a lone
// "a" compare against the merge base.
func ParseRange(args []string) (Options, error) {
	switch len(args) {
	case 0:
		return Options{MergeBase: true}, nil
	case 1:
		if base, head, ok := strings.Cut(args[0], "..."); ok {
			return Options{Base: orHEAD(base), Head: orHEAD(head), MergeBase: true}, nil
		}
		if base, head, ok := strings.Cut(args[0], ".."); ok {
			return Options{Base: orHEAD(base), Head: orHEAD(head)}, nil
		}
		return Options{Base: args[0], MergeBase: true}, nil
	case maxRevisions:
		return Options{Base: args[0], Head: args[1], MergeBase: true}, nil
	}
	return Options{}, fmt.Errorf("expected at most %d revisions, got %d", maxRevisions, len(args))
}

// orHEAD fills an omitted side of a range the way git does.
func orHEAD(rev string) string {
	if rev == "" {
		return headRef
	}
	return rev
}

var statusNames = map[byte]string{
	'A': review.Added,
	'M': review.Modified,
	'D': review.Deleted,
	'R': review.Renamed,
	'C': review.Copied,
	'T': review.TypeChanged,
}

// Build analyses and groups the range selected by opts in the repository at
// dir. Declarations and links between files come from an outline graph of
// the head tree; if that graph can't be built the overview carries a warning
// and is grouped without it. If ctx ends, Build returns ctx.Err().
func Build(ctx context.Context, dir string, opts Options) (*review.Overview, error) {
	o, err := build(ctx, dir, opts)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return o, err
}

func build(ctx context.Context, dir string, opts Options) (*review.Overview, error) {
	git := gitRunner{dir: dir}
	rng, err := resolveRange(ctx, git, opts)
	if err != nil {
		return nil, err
	}
	diffs, err := git.fileDiffs(ctx, rng)
	if err != nil {
		return nil, err
	}
	o := &review.Overview{Range: &rng, Files: make([]review.File, 0, len(diffs)), Branch: git.branch(ctx, rng.Head)}

	if len(diffs) > 0 {
		git.addGraphs(ctx, o, rng, diffs)
	}

	for _, d := range diffs {
		f, err := review.Analyze(d)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d.Path, err)
		}
		o.Add(f)
	}
	o.Group()
	return o, nil
}

// fileDiffs reads every changed file with both sides' contents.
func (g gitRunner) fileDiffs(ctx context.Context, rng review.Range) ([]review.FileDiff, error) {
	g, cleanup, err := g.diffRepository(ctx, rng.HeadRev)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	raw, err := g.rawChanges(ctx, rng.BaseRev, rng.HeadRev)
	if err != nil {
		return nil, err
	}
	counts, err := g.numstat(ctx, rng.BaseRev, rng.HeadRev)
	if err != nil {
		return nil, err
	}
	hunks, err := g.hunks(ctx, rng.BaseRev, rng.HeadRev)
	if err != nil {
		return nil, err
	}
	if len(counts) != len(raw) {
		return nil, fmt.Errorf("git diff outputs disagree: %d raw, %d numstat", len(raw), len(counts))
	}
	blobs, err := g.blobs(ctx)
	if err != nil {
		return nil, err
	}
	diffs := make([]review.FileDiff, 0, len(raw))
	for i, c := range raw {
		d, err := fileDiff(c, counts[i], hunks[c.path], blobs)
		if err != nil {
			_ = blobs.close()
			return nil, fmt.Errorf("%s: %w", c.path, err)
		}
		diffs = append(diffs, d)
	}
	return diffs, blobs.close()
}

func resolveRange(ctx context.Context, git gitRunner, opts Options) (review.Range, error) {
	rng := review.Range{Base: opts.Base, Head: opts.Head, MergeBase: opts.MergeBase}
	if rng.Base == "" {
		branch, err := git.defaultBranch(ctx)
		if err != nil {
			return rng, err
		}
		rng.Base = branch
	}
	if rng.Head == "" {
		rng.Head = headRef
	}
	var err error
	if rng.HeadRev, err = git.revParse(ctx, rng.Head); err != nil {
		return rng, err
	}
	if rng.BaseRev, err = git.revParse(ctx, rng.Base); err != nil {
		return rng, err
	}
	if rng.MergeBase {
		if rng.BaseRev, err = git.mergeBase(ctx, rng.BaseRev, rng.HeadRev); err != nil {
			return rng, fmt.Errorf("no merge base between %s and %s: %w", rng.Base, rng.Head, err)
		}
	}
	return rng, nil
}

func fileDiff(c rawChange, counts lineCounts, hunks []review.Hunk, blobs *blobReader) (review.FileDiff, error) {
	d := review.FileDiff{
		Path:      c.path,
		OldPath:   c.oldPath,
		Status:    statusNames[c.status],
		Additions: counts.additions,
		Deletions: counts.deletions,
		Binary:    counts.binary,
		OldMode:   presentMode(c.oldMode),
		NewMode:   presentMode(c.newMode),
		Symlink:   c.oldMode == symlinkMode || c.newMode == symlinkMode,
		Submodule: c.oldMode == gitlinkMode || c.newMode == gitlinkMode,
		Hunks:     hunks,
	}
	if d.Status == "" {
		d.Status = string(c.status)
	}
	if c.status == 'R' || c.status == 'C' {
		d.Similarity = c.score
	}
	if d.Submodule {
		return d, nil
	}
	var err error
	if c.status != 'A' {
		if d.Base, err = blobs.content(c.oldOID); err != nil {
			return d, err
		}
	}
	if c.status != 'D' {
		if c.newOID == c.oldOID && d.Base != nil {
			d.Head = d.Base
		} else if d.Head, err = blobs.content(c.newOID); err != nil {
			return d, err
		}
	}
	return d, nil
}

// presentMode drops the all-zero mode git reports for a missing side.
func presentMode(mode string) string {
	if strings.Trim(mode, "0") == "" {
		return ""
	}
	return mode
}

func (b *blobReader) content(oid string) (*review.Content, error) {
	data, size, err := b.read(oid, review.MaxContentBytes)
	if err != nil {
		return nil, err
	}
	return &review.Content{Data: data, Complete: int64(len(data)) == size}, nil
}

// Diff writes git's patch for paths between the two commits of rng. Paths are
// relative to the repository root, as in an overview, wherever dir is.
func Diff(ctx context.Context, dir string, rng review.Range, paths []string, w io.Writer) error {
	git, cleanup, err := (gitRunner{dir: dir}).diffRepository(ctx, rng.HeadRev)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer cleanup()
	args := append([]string{gitDiff}, diffFlags...)
	args = append(args, rng.BaseRev, rng.HeadRev, "--")
	for _, p := range paths {
		args = append(args, rootPathspec(p))
	}
	cmd := git.command(ctx, args...)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("git diff: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}
