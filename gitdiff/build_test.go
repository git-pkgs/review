package gitdiff

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/review"
	"github.com/git-pkgs/review/internal/gittest"
	"github.com/git-pkgs/roles"
)

// featureRepo has a branch "feature" checked out, forked from main, with main
// moved on by one unrelated commit since.
func featureRepo(t *testing.T) *gittest.Repo {
	r := gittest.New(t)
	r.Write("parser.go", "package p\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n")
	r.Write("server.go", "package p\n\nfunc Serve() {\n\tprintln(\"a\")\n}\n")
	r.Write("old_name.go", "package p\n\nfunc Moved() int {\n\tx := 1\n\ty := 2\n\tz := 3\n\treturn x + y + z\n}\n")
	r.Write("gone.go", "package p\n\nfunc Gone() {}\n")
	r.Write("pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	r.Write("vendor/lib/lib.go", "package lib\n\nfunc Lib() {}\n")
	r.Write("docs/guide.md", "# Guide\n\n## Install\n\nRun it.\n")
	r.Write("logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00one")
	r.Commit("base")

	r.Git("checkout", "-q", "-b", "feature")
	r.Write("parser.go", "package p\n\nfunc Parse(s string, strict bool) int {\n\treturn len(s)\n}\n")
	r.Write("server.go", "package p\n\nfunc Serve() {\n\tprintln(\"b\")\n}\n")
	r.Git("mv", "old_name.go", "new_name.go")
	r.Git("rm", "-q", "gone.go")
	r.Write("pnpm-lock.yaml", "lockfileVersion: '9.0'\nsettings: {}\n")
	r.Write("vendor/lib/lib.go", "package lib\n\nfunc Lib() { println() }\n")
	r.Write("docs/guide.md", "# Guide\n\n## Install\n\nRun it twice.\n")
	r.Write("logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00two")
	r.Write("added.go", "package p\n\n// Thing is new.\ntype Thing struct {\n\tA int\n}\n\nfunc Added() {}\n")
	r.Write("parser_test.go", "package p\n\nimport \"testing\"\n\nfunc TestParse(t *testing.T) {}\n")
	r.Commit("feature")

	r.Git("checkout", "-q", "main")
	r.Write("unrelated.go", "package p\n")
	r.Commit("unrelated")
	r.Git("checkout", "-q", "feature")
	return r
}

func fileByPath(t *testing.T, o *review.Overview, path string) review.File {
	t.Helper()
	for _, f := range o.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no file %s in overview", path)
	return review.File{}
}

func TestBuildFeatureBranch(t *testing.T) {
	r := featureRepo(t)
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.Range.BaseRev != r.Git("merge-base", "main", "feature") {
		t.Errorf("base rev = %s, want merge base", o.Range.BaseRev)
	}
	for _, f := range o.Files {
		if f.Path == "unrelated.go" {
			t.Error("merge-base range includes a change made only on main")
		}
	}
	if len(o.Files) != 10 {
		t.Errorf("got %d files, want 10", len(o.Files))
	}
}

func featureOverview(t *testing.T) *review.Overview {
	t.Helper()
	o, err := Build(context.Background(), featureRepo(t).Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestBuildDeclarations(t *testing.T) {
	o := featureOverview(t)
	parser := fileByPath(t, o, "parser.go")
	if parser.Status != "modified" || parser.Additions != 1 || parser.Deletions != 1 {
		t.Errorf("parser.go = %+v", parser)
	}
	if parser.Surface == nil || parser.Surface.BodyOnly() {
		t.Fatalf("parser.go surface = %+v, want signature change", parser.Surface)
	}
	want := []review.SurfaceLine{
		{Op: "-", Text: "func Parse(s string) int {"},
		{Op: "+", Text: "func Parse(s string, strict bool) int {"},
	}
	if !slices.Equal(parser.Surface.Lines, want) {
		t.Errorf("parser.go lines = %+v", parser.Surface.Lines)
	}

	server := fileByPath(t, o, "server.go")
	if server.Surface == nil || !server.Surface.BodyOnly() {
		t.Errorf("server.go surface = %+v, want body only", server.Surface)
	}
	if !slices.Equal(server.Contexts, []string{"func Serve() {"}) {
		t.Errorf("server.go contexts = %q", server.Contexts)
	}

	moved := fileByPath(t, o, "new_name.go")
	if moved.Status != "renamed" || moved.OldPath != "old_name.go" || moved.Similarity != 100 || moved.Surface != nil {
		t.Errorf("new_name.go = %+v", moved)
	}

	gone := fileByPath(t, o, "gone.go")
	if gone.Status != "deleted" || gone.Surface == nil || len(gone.Surface.Lines) == 0 || gone.Surface.Lines[0].Op != "-" {
		t.Errorf("gone.go = %+v", gone)
	}

	added := fileByPath(t, o, "added.go")
	if added.Status != "added" || added.Surface == nil {
		t.Fatalf("added.go = %+v", added)
	}
	if !slices.Contains(added.Surface.Lines, review.SurfaceLine{Op: "+", Text: "A int", Context: "type Thing struct {"}) {
		t.Errorf("added.go lines = %+v", added.Surface.Lines)
	}
}

func TestBuildRolesAndSkips(t *testing.T) {
	o := featureOverview(t)
	if test := fileByPath(t, o, "parser_test.go"); !slices.Contains(test.Roles, roles.Test) {
		t.Errorf("parser_test.go roles = %v", test.Roles)
	}
	if guide := fileByPath(t, o, "docs/guide.md"); !slices.Equal(guide.Contexts, []string{"## Install"}) {
		t.Errorf("docs/guide.md contexts = %q", guide.Contexts)
	}

	for path, skip := range map[string]string{
		"pnpm-lock.yaml":    "generated (lockfile)",
		"vendor/lib/lib.go": "vendor",
		"logo.png":          "binary",
	} {
		f := fileByPath(t, o, path)
		if f.Skip != skip {
			t.Errorf("%s skip = %q, want %q", path, f.Skip, skip)
		}
		if f.Surface != nil {
			t.Errorf("%s has a surface diff despite being skipped", path)
		}
	}
	if logo := fileByPath(t, o, "logo.png"); !logo.Binary {
		t.Error("logo.png not marked binary")
	}
}

func TestBuildTwoDotRange(t *testing.T) {
	r := featureRepo(t)
	opts, err := ParseRange([]string{"main..feature"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := Build(context.Background(), r.Dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if f := fileByPath(t, o, "unrelated.go"); f.Status != "deleted" {
		t.Errorf("unrelated.go status = %q, want deleted in a two-dot range", f.Status)
	}
}

func TestBuildDefaultsToMainAndHEAD(t *testing.T) {
	r := featureRepo(t)
	o, err := Build(context.Background(), r.Dir, Options{MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.Range.Base != "main" || o.Range.Head != "HEAD" {
		t.Errorf("range = %+v", o.Range)
	}
}

func TestBuildUnknownRevision(t *testing.T) {
	r := featureRepo(t)
	_, err := Build(context.Background(), r.Dir, Options{Base: "nope", MergeBase: true})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v", err)
	}
}

func TestBuildPathsWithSpaces(t *testing.T) {
	r := gittest.New(t)
	r.Write("a dir/one file.go", "package p\n\nfunc A() {}\n")
	r.Commit("base")
	r.Git("mv", "a dir/one file.go", "a dir/two\tfile.go")
	r.Commit("rename")
	o, err := Build(context.Background(), r.Dir, Options{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	f := fileByPath(t, o, "a dir/two\tfile.go")
	if f.OldPath != "a dir/one file.go" {
		t.Errorf("old path = %q", f.OldPath)
	}
}

func TestOverviewText(t *testing.T) {
	r := featureRepo(t)
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := o.Text(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"feature\nmain...HEAD (",
		"), 1 commit, 10 files, +18 -7\n",
		"1. Changes to added.go, gone.go and 1 other file  (3 files, +9 -4)\n" +
			"   Adds added.go. Deletes gone.go. Adds type `Thing`, and function `Added`. Changes the\n" +
			"   body of function `Serve`. Removes `func Gone() {}`. No test files changed.\n",
		"2. Changes to parser.go and tests  (2 files, +6 -1)\n" +
			"   Adds parser_test.go. Changes the declaration of function `Parse`. Includes 1 test\n   file.\n",
		"3. Documentation  (1 file, +1 -1)\n   Changes docs/guide.md (## Install).\n",
		"4. Renamed without changes  (1 file)\n",
		"5. Binary, generated and vendored files  (3 files, +2 -1)\n" +
			"   Binary: logo.png. Generated (lockfile): pnpm-lock.yaml. Vendor: vendor/lib/lib.go.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n%s", want, out)
		}
	}
}

// moduleRepo is a Go module whose feature branch changes Load's signature in
// lib and updates its caller in api, plus an unrelated edit in cli.
func moduleRepo(t *testing.T) *gittest.Repo {
	r := gittest.New(t)
	r.Write("go.mod", "module example.com/m\n\ngo 1.26\n")
	r.Write("lib/store.go", "package lib\n\nfunc Load() int {\n\treturn 1\n}\n")
	r.Write("api/handler.go", "package api\n\nimport \"example.com/m/lib\"\n\nfunc Handle() int {\n\treturn lib.Load()\n}\n")
	r.Write("cli/main.go", "package main\n\nfunc main() {\n\tprintln(1)\n}\n")
	r.Commit("base")
	r.Git("checkout", "-q", "-b", "feature")
	r.Write("lib/store.go", "package lib\n\nfunc Load(strict bool) int {\n\treturn 1\n}\n")
	r.Write("api/handler.go", "package api\n\nimport \"example.com/m/lib\"\n\nfunc Handle() int {\n\treturn lib.Load(true)\n}\n")
	r.Write("cli/main.go", "package main\n\nfunc main() {\n\tprintln(2)\n}\n")
	r.Commit("Make Load strict")
	return r
}

func TestBuildLinksFromGraph(t *testing.T) {
	r := moduleRepo(t)
	for name, prepare := range map[string]func(){
		"head checked out": func() {},
		"main checked out": func() { r.Git("checkout", "-q", "main") },
	} {
		t.Run(name, func(t *testing.T) {
			prepare()
			o, err := Build(context.Background(), r.Dir, Options{Base: "main", Head: "feature", MergeBase: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(o.Warnings) > 0 {
				t.Fatalf("warnings = %q", o.Warnings)
			}
			if !slices.Contains(o.Links, review.Link{From: "api/handler.go", To: "lib/store.go"}) {
				t.Errorf("links = %+v", o.Links)
			}
			if slices.ContainsFunc(o.Links, func(l review.Link) bool { return l.From == "cli/main.go" || l.To == "cli/main.go" }) {
				t.Errorf("unrelated file linked: %+v", o.Links)
			}
			store := fileByPath(t, o, "lib/store.go")
			if !slices.Equal(store.Symbols, []review.SymbolChange{{Name: "Load", Kind: "func", Exported: true, Change: review.SymbolSignature}}) {
				t.Errorf("store symbols = %+v", store.Symbols)
			}
			handler := fileByPath(t, o, "api/handler.go")
			if !slices.Equal(handler.Symbols, []review.SymbolChange{{Name: "Handle", Kind: "func", Exported: true, Change: review.SymbolBody}}) {
				t.Errorf("handler symbols = %+v", handler.Symbols)
			}
			if len(o.Groups) != 2 || !slices.Equal(o.Groups[0].Files, []string{"api/handler.go", "lib/store.go"}) {
				t.Errorf("groups = %+v", o.Groups)
			}
		})
	}
}

func TestBuildCommitsAndBranch(t *testing.T) {
	r := moduleRepo(t)
	r.Write("cli/main.go", "package main\n\nfunc main() {\n\tprintln(3)\n\tprintln(4)\n\tprintln(5)\n}\n")
	r.Commit("Print more")
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.Branch != "feature" || len(o.Commits) != 2 {
		t.Fatalf("branch %q, commits %+v", o.Branch, o.Commits)
	}
	if c := o.Commits[1]; c.Subject != "Print more" || len(c.Files) != 1 || c.Files[0].Path != "cli/main.go" {
		t.Errorf("second commit = %+v", c)
	}
	titles := []string{}
	for _, g := range o.Groups {
		titles = append(titles, g.Title)
	}
	if !slices.Equal(titles, []string{"Make Load strict", "Print more"}) {
		t.Errorf("titles = %q", titles)
	}
}

func TestDiff(t *testing.T) {
	r := moduleRepo(t)
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Diff(context.Background(), r.Dir, *o.Range, []string{"cli/main.go"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "+\tprintln(2)") || strings.Contains(buf.String(), "lib/store.go") {
		t.Errorf("diff = %s", buf.String())
	}
}

func TestParseRange(t *testing.T) {
	tests := []struct {
		args []string
		want Options
	}{
		{nil, Options{MergeBase: true}},
		{[]string{"main"}, Options{Base: "main", MergeBase: true}},
		{[]string{"main...topic"}, Options{Base: "main", Head: "topic", MergeBase: true}},
		{[]string{"main..topic"}, Options{Base: "main", Head: "topic"}},
		{[]string{"main.."}, Options{Base: "main", Head: "HEAD"}},
		{[]string{"...topic"}, Options{Base: "HEAD", Head: "topic", MergeBase: true}},
		{[]string{"main", "topic"}, Options{Base: "main", Head: "topic", MergeBase: true}},
	}
	for _, tt := range tests {
		got, err := ParseRange(tt.args)
		if err != nil {
			t.Errorf("ParseRange(%q): %v", tt.args, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseRange(%q) = %+v, want %+v", tt.args, got, tt.want)
		}
	}
	if _, err := ParseRange([]string{"a", "b", "c"}); err == nil {
		t.Error("three revisions accepted")
	}
}

// A patch of the same range must agree with the git reading on everything a
// patch can carry.
func TestBuildMatchesPatch(t *testing.T) {
	r := featureRepo(t)
	fromGit, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	patch := r.Git("diff", "--find-renames", "main...feature")
	fromPatch, err := review.FromPatch(strings.NewReader(patch + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(fromPatch.Files) != len(fromGit.Files) {
		t.Fatalf("patch has %d files, git has %d", len(fromPatch.Files), len(fromGit.Files))
	}
	if fromPatch.Additions != fromGit.Additions || fromPatch.Deletions != fromGit.Deletions {
		t.Errorf("patch totals +%d -%d, git +%d -%d", fromPatch.Additions, fromPatch.Deletions, fromGit.Additions, fromGit.Deletions)
	}
	for _, g := range fromGit.Files {
		p := fileByPath(t, fromPatch, g.Path)
		if p.Status != g.Status || p.OldPath != g.OldPath || p.Similarity != g.Similarity ||
			p.Additions != g.Additions || p.Deletions != g.Deletions || p.Binary != g.Binary ||
			p.Skip != g.Skip || !slices.Equal(p.Roles, g.Roles) {
			t.Errorf("%s:\npatch %+v\ngit   %+v", g.Path, p, g)
		}
		if g.Status == review.Added || g.Status == review.Deleted {
			if (p.Surface == nil) != (g.Surface == nil) || (p.Surface != nil && !slices.Equal(p.Surface.Lines, g.Surface.Lines)) {
				t.Errorf("%s surface: patch %+v, git %+v", g.Path, p.Surface, g.Surface)
			}
		}
	}
}

func TestBuildGraphIgnoresLocalEdits(t *testing.T) {
	r := moduleRepo(t)
	clean, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	r.Write("go.mod", "module example.com/unrelated\n\ngo 1.26\n")
	r.Write("lib/extra.go", "package lib\n\nfunc Extra() {}\n")
	dirty, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Links) == 0 || !slices.Equal(clean.Links, dirty.Links) {
		t.Errorf("links changed with local edits: clean %+v, dirty %+v", clean.Links, dirty.Links)
	}
}

func TestBuildGraphKeepsExportIgnoredFiles(t *testing.T) {
	r := moduleRepo(t)
	r.Write(".gitattributes", "lib export-ignore\n")
	r.Commit("ignore lib in archives")
	r.Write("untracked.txt", "dirty\n")
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Links, review.Link{From: "api/handler.go", To: "lib/store.go"}) {
		t.Errorf("links = %+v", o.Links)
	}
}

func TestBuildReorderIsNotWhitespace(t *testing.T) {
	r := gittest.New(t)
	r.Write("flow.go", "package flow\n\nfunc Run() {\n\tauthorize()\n\texecute()\n}\n")
	r.Commit("base")
	r.Write("flow.go", "package flow\n\nfunc Run() {\n\texecute()\n\tauthorize()\n}\n")
	r.Commit("reorder")
	o, err := Build(context.Background(), r.Dir, Options{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if f := fileByPath(t, o, "flow.go"); f.Mechanical != "" {
		t.Errorf("reordered calls classified as %q", f.Mechanical)
	}
}

func TestBuildSummaryKeepsRemovedDeclarations(t *testing.T) {
	r := gittest.New(t)
	r.Write("api.go", "package api\n\nfunc Removed() {}\n\nfunc Kept() int {\n\treturn 1\n}\n")
	r.Commit("base")
	r.Write("api.go", "package api\n\nfunc Kept() int {\n\treturn 2\n}\n")
	r.Commit("update API")
	o, err := Build(context.Background(), r.Dir, Options{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(o.Groups[0].Summary, " ")
	if !strings.Contains(got, "Removes `func Removed() {}`.") || !strings.Contains(got, "Changes the body of function `Kept`.") {
		t.Errorf("summary = %q", got)
	}
}

func TestBuildJoinedLinesAreNotWhitespace(t *testing.T) {
	r := gittest.New(t)
	r.Write("flow.go", "package flow\n\nfunc Run() {\n\t// authorize\n\texecute()\n}\n")
	r.Commit("base")
	r.Write("flow.go", "package flow\n\nfunc Run() {\n\t// authorize execute()\n}\n")
	r.Commit("disable execution")
	o, err := Build(context.Background(), r.Dir, Options{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if f := fileByPath(t, o, "flow.go"); f.Mechanical != "" {
		t.Errorf("joined lines classified as %q", f.Mechanical)
	}
}

func TestBuildTypeChange(t *testing.T) {
	r := gittest.New(t)
	r.Write("link", "contents\n")
	r.Commit("base")
	if err := os.Remove(filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	r.Commit("replace with symlink")
	o, err := Build(context.Background(), r.Dir, Options{Base: "HEAD~1", Head: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	f := fileByPath(t, o, "link")
	if f.Status != review.TypeChanged || f.Hunks != 2 {
		t.Errorf("link = %+v", f)
	}
	if got := strings.Join(o.Groups[0].Summary, " "); !strings.Contains(got, "Replaces link with a symbolic link.") {
		t.Errorf("summary = %q", got)
	}
}

func TestBuildGraphFromSubdirectory(t *testing.T) {
	r := moduleRepo(t)
	r.Write("untracked.txt", "forces the head to be written out\n")
	o, err := Build(context.Background(), filepath.Join(r.Dir, "api"), Options{Base: "main", Head: "feature", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Links, review.Link{From: "api/handler.go", To: "lib/store.go"}) {
		t.Errorf("links from a subdirectory = %+v, warnings %q", o.Links, o.Warnings)
	}
}

func TestBuildGraphIgnoresCheckoutFilters(t *testing.T) {
	r := moduleRepo(t)
	r.Write(".git/info/attributes", "go.mod filter=rename\n")
	r.Git("config", "filter.rename.smudge", "sed s,example.com/m,example.com/other,g")
	r.Write("untracked.txt", "forces the head to be written out\n")
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", Head: "feature", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Links, review.Link{From: "api/handler.go", To: "lib/store.go"}) {
		t.Errorf("links with a smudge filter = %+v", o.Links)
	}
}

func TestBuildGraphIgnoresCleanFilters(t *testing.T) {
	r := moduleRepo(t)
	r.Write(".git/info/attributes", "go.mod filter=rename\n")
	r.Git("config", "filter.rename.clean", "sed s,example.com/other,example.com/m,g")
	r.Git("config", "filter.rename.smudge", "sed s,example.com/m,example.com/other,g")
	if err := os.Remove(filepath.Join(r.Dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	r.Git("checkout", "--", "go.mod")
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "go.mod")); !strings.Contains(string(got), "example.com/other") {
		t.Fatalf("smudge filter didn't run: %q", got)
	}
	if status := r.Git("status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("fixture isn't clean: %q", status)
	}
	o, err := Build(context.Background(), r.Dir, Options{Base: "main", MergeBase: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(o.Links, review.Link{From: "api/handler.go", To: "lib/store.go"}) {
		t.Errorf("links with a filtered clean worktree = %+v", o.Links)
	}
}
