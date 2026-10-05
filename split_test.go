package review

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestTestSubject(t *testing.T) {
	tests := []struct {
		test  string
		files []string
		want  string
	}{
		{"spec/models/user_spec.rb", []string{"app/serializers/user.rb", "app/models/user.rb"}, "app/models/user.rb"},
		{"src/test/java/org/x/FooTest.java", []string{"src/main/java/org/x/Foo.java", "src/main/java/org/y/Foo.java"}, "src/main/java/org/x/Foo.java"},
		{"web/__tests__/app.test.ts", []string{"web/app.ts", "api/app.ts"}, "web/app.ts"},
		{"tests/test_parser.py", []string{"mypkg/parser.py"}, "mypkg/parser.py"},
		{"lib/foo_test.go", []string{"lib/foo.go", "other/foo.go"}, "lib/foo.go"},
		{"test/foo_test.rb", []string{"lib/a/foo.rb", "lib/b/foo.rb"}, ""},
		{"test/foo_test.rb", []string{"lib/bar.rb"}, ""},
	}
	for _, tt := range tests {
		o := analyzed(t, append([]FileDiff{mod(tt.test, 1, 0)}, modAll(tt.files)...)...)
		files := make([]*File, len(o.Files))
		for i := range o.Files {
			files[i] = &o.Files[i]
		}
		got := ""
		if i, ok := testSubject(files[0], files); ok {
			got = files[i].Path
		}
		if got != tt.want {
			t.Errorf("testSubject(%s, %q) = %q, want %q", tt.test, tt.files, got, tt.want)
		}
	}
}

func modAll(paths []string) []FileDiff {
	out := make([]FileDiff, 0, len(paths))
	for _, p := range paths {
		out = append(out, mod(p, 1, 0))
	}
	return out
}

func monorepo(t *testing.T) *Overview {
	t.Helper()
	var paths []string
	for i := range 6 {
		paths = append(paths, fmt.Sprintf("packages/app/src/view%d.ts", i))
	}
	for i := range 4 {
		paths = append(paths, fmt.Sprintf("packages/cli/src/cmd%d.ts", i))
	}
	paths = append(paths,
		"packages/core/src/source.ts",
		"packages/core/src/repo.ts",
		"packages/core/src/types.ts",
		"packages/core/test/source.test.ts",
		"packages/cli/test/cmd0.test.ts",
		"plans/local.md",
	)
	// Distinct sizes, so no directory looks like one edit repeated.
	var diffs []FileDiff
	o := &Overview{}
	o.Commits = []Commit{{Subject: "Serve local reviews"}}
	for i, p := range paths {
		diffs = append(diffs, mod(p, i+1, 0))
		o.Commits[0].Files = append(o.Commits[0].Files, CommitFile{Path: p, Additions: i + 1})
	}
	for _, f := range analyzed(t, diffs...).Files {
		o.Add(f)
	}
	o.Commits = append(o.Commits, Commit{Subject: "Unrelated", Files: []CommitFile{{Path: "elsewhere.go", Additions: 1}}})
	return o
}

func TestSplitLargeGroup(t *testing.T) {
	o := monorepo(t)
	o.Group()
	g := o.Groups[0]
	if g.Title != "Serve local reviews" || len(g.Children) != 4 {
		t.Fatalf("group = %+v", g)
	}
	byTitle := map[string][]string{}
	for i, c := range g.Children {
		if c.ID != i+1 {
			t.Errorf("child %d has id %d", i, c.ID)
		}
		byTitle[c.Title] = c.Files
	}
	if fs := byTitle["Changes in packages/core/ and tests"]; !slices.Contains(fs, "packages/core/test/source.test.ts") || len(fs) != 4 {
		t.Errorf("core part = %q (titles %v)", fs, g.Children)
	}
	if fs := byTitle["Changes in packages/cli/ and tests"]; !slices.Contains(fs, "packages/cli/test/cmd0.test.ts") {
		t.Errorf("cli part = %q", fs)
	}
	if fs := byTitle["Other files"]; !slices.Equal(fs, []string{"plans/local.md"}) {
		t.Errorf("other part = %q", fs)
	}
	if last := g.Children[len(g.Children)-1]; last.Title != "Other files" {
		t.Errorf("other files not last: %q", last.Title)
	}
	if g.Summary[0] != "In 4 parts by directory: packages/app/, packages/cli/, packages/core/ and other files." {
		t.Errorf("summary = %q", g.Summary)
	}

	var buf bytes.Buffer
	if err := o.Render(&buf, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\n   1.1 Changes in packages/core/ and tests  (4 files, +50 -0)\n") ||
		!strings.Contains(buf.String(), "\n        M packages/app/src/view0.ts  +1 -0\n") {
		t.Errorf("render:\n%s", buf.String())
	}
}

func TestSplitLeavesSmallOrFlatGroups(t *testing.T) {
	small := analyzed(t, modAll([]string{"a/1.go", "a/2.go", "a/3.go", "b/1.go", "b/2.go", "b/3.go"})...)
	small.Group()
	for _, g := range small.Groups {
		if len(g.Children) > 0 {
			t.Errorf("small group split: %+v", g)
		}
	}
	var flatDiffs []FileDiff
	for i := range 15 {
		flatDiffs = append(flatDiffs, mod(fmt.Sprintf("pkg/f%d.go", i), i+1, 0))
	}
	o := analyzed(t, flatDiffs...)
	o.Group()
	if len(o.Groups) != 1 || len(o.Groups[0].Children) > 0 {
		t.Errorf("one-directory group split: %+v", o.Groups)
	}
}
