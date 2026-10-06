package gitdiff

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/git-pkgs/review"
	"github.com/git-pkgs/review/internal/gittest"
)

// TestModesAgree applies random sequences of edits, additions, deletions and
// renames, including renames onto freed paths, and requires the repository
// and net patch views of each range to report the same files and groups.
// REVIEW_MODES_SEQUENCES raises the number of sequences tried.
func TestModesAgree(t *testing.T) {
	sequences := 30
	if n, err := strconv.Atoi(os.Getenv("REVIEW_MODES_SEQUENCES")); err == nil {
		sequences = n
	}
	for seed := range sequences {
		r, steps := randomHistory(t, uint64(seed))
		rng := fmt.Sprintf("HEAD~%d..HEAD", len(steps))
		opts, err := ParseRange([]string{rng})
		if err != nil {
			t.Fatal(err)
		}
		fromGit, err := Build(context.Background(), r.Dir, opts)
		if err != nil {
			t.Fatal(err)
		}
		patch := r.Git("diff", rng) + "\n"
		fromPatch, err := review.FromPatch(strings.NewReader(patch))
		if err != nil {
			t.Fatal(err)
		}
		gotGit, gotPatch := describeModes(fromGit), describeModes(fromPatch)
		if !slices.Equal(gotGit, gotPatch) {
			t.Errorf("seed %d, steps %q:\n git:   %q\n patch: %q", seed, steps, gotGit, gotPatch)
		}
	}
}

// randomHistory commits a few files, then three to five commits that each
// make one random change, and returns the changes.
func randomHistory(t *testing.T, seed uint64) (*gittest.Repo, []string) {
	t.Helper()
	random := rand.New(rand.NewPCG(seed, 1))
	r := gittest.New(t)
	contents := map[string]string{}
	next := 0
	write := func(path string) {
		next++
		var b strings.Builder
		b.WriteString("package p\n")
		for line := range 12 {
			fmt.Fprintf(&b, "// file %d line %d\n", next, line)
		}
		contents[path] = b.String()
		r.Write(path, contents[path])
	}
	paths := []string{"a.go", "b.go", "c.go", "d.go", "e.go"}
	for _, p := range paths[:3] {
		write(p)
	}
	r.Commit("base")

	live := func() []string {
		var out []string
		for _, p := range paths {
			if _, ok := contents[p]; ok {
				out = append(out, p)
			}
		}
		return out
	}
	free := func() []string {
		var out []string
		for _, p := range paths {
			if _, ok := contents[p]; !ok {
				out = append(out, p)
			}
		}
		return out
	}
	pick := func(from []string) string { return from[random.IntN(len(from))] }

	var steps []string
	for i := range 3 + random.IntN(3) {
		var step string
		switch op := random.IntN(4); {
		case op == 0 && len(free()) > 0:
			p := pick(free())
			write(p)
			step = "add " + p
		case op == 1 && len(live()) > 1:
			p := pick(live())
			r.Git("rm", "-q", p)
			delete(contents, p)
			step = "delete " + p
		case op == 2 && len(free()) > 0:
			from, to := pick(live()), pick(free())
			r.Git("mv", from, to)
			contents[to] = contents[from]
			delete(contents, from)
			step = "rename " + from + " " + to
		default:
			p := pick(live())
			contents[p] += fmt.Sprintf("// edit %d\n", i)
			r.Write(p, contents[p])
			step = "edit " + p
		}
		r.Commit(step)
		steps = append(steps, step)
	}
	return r, steps
}

// describeModes lists an overview's files and groups in a form both modes
// share: paths, statuses, net line counts and which files each group holds.
func describeModes(o *review.Overview) []string {
	var out []string
	for _, f := range o.Files {
		s := fmt.Sprintf("file %s %s +%d -%d", f.Path, f.Status, f.Additions, f.Deletions)
		if f.OldPath != "" {
			s += " from " + f.OldPath
		}
		out = append(out, s)
	}
	for _, g := range o.Groups {
		out = append(out, "group "+g.Title+": "+strings.Join(g.Files, ","))
	}
	slices.Sort(out)
	return out
}
