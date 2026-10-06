package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/review/internal/gittest"
)

func TestCommittedDiffIgnoresLocalAttributes(t *testing.T) {
	for _, location := range []string{".gitattributes", "lib/.gitattributes", ".git/info/attributes", "global-attributes"} {
		t.Run(location, func(t *testing.T) {
			r := branchRepo(t)
			commands := [][]string{{"overview", "--json"}, {"show", "1"}}
			want := make([]string, len(commands))
			for i, command := range commands {
				var err error
				want[i], err = runCLI(t, "", append(command, "-C", r.Dir, "main..HEAD")...)
				if err != nil {
					t.Fatal(err)
				}
			}
			r.Write(location, "*.go -diff\n")
			if location == "global-attributes" {
				r.Git("config", "core.attributesFile", r.Dir+"/"+location)
			}
			for i, command := range commands {
				got, err := runCLI(t, "", append(command, "-C", r.Dir, "main..HEAD")...)
				if err != nil || got != want[i] {
					t.Errorf("%v with local attributes: %v\ngot:\n%s\nwant:\n%s", command, err, got, want[i])
				}
			}
		})
	}
}

func TestDiffAttributesWithSharedObjects(t *testing.T) {
	r := branchRepo(t)
	want, err := runCLI(t, "", "show", "1", "-C", r.Dir, "main..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	r.Git("worktree", "add", "--detach", worktree, "HEAD")
	bare := filepath.Join(t.TempDir(), "bare.git")
	r.Git("clone", "--bare", "--shared", r.Dir, bare)
	r.Write(".git/info/attributes", "*.go -diff\n")
	for _, dir := range []string{worktree, bare} {
		out, err := runCLI(t, "", "show", "1", "-C", dir, "main..HEAD")
		if err != nil || out != want {
			t.Errorf("show in %s: %v\n%s", dir, err, out)
		}
	}
}

func TestDiffUsesSelectedHeadAttributes(t *testing.T) {
	r := gittest.New(t)
	r.Write(".gitattributes", "*.go -diff\n")
	r.Write("lib/value.go", "package lib\n\nfunc Value() int { return 1 }\n")
	base := r.Commit("binary attributes")
	r.Write(".gitattributes", "*.go diff=golang\n")
	r.Write("lib/value.go", "package lib\n\nfunc Value() int { return 2 }\n")
	head := r.Commit("source attributes")
	for _, checkout := range []string{base, head} {
		r.Git("checkout", "-q", checkout)
		// Both the index and worktree disagree with the selected text revision.
		r.Write(".gitattributes", "*.go -diff\n")
		r.Git("add", ".gitattributes")
		for _, tc := range []struct {
			rng    string
			binary bool
		}{{base + ".." + head, false}, {head + ".." + base, true}} {
			out, err := runCLI(t, "", "overview", "-C", r.Dir, tc.rng)
			if err != nil || strings.Contains(out, "Binary files") != tc.binary {
				t.Fatalf("overview %s: %v\n%s", tc.rng, err, out)
			}
			group := "1"
			if tc.binary {
				group = "2"
			}
			out, err = runCLI(t, "", "show", group, "-C", r.Dir, tc.rng)
			if err != nil || strings.Contains(out, "Binary files a/lib/value.go and b/lib/value.go differ") != tc.binary {
				t.Fatalf("show %s: %v\n%s", tc.rng, err, out)
			}
			if !tc.binary && !strings.Contains(out, "+func Value() int { return 2 }") {
				t.Fatalf("missing source patch:\n%s", out)
			}
		}
		r.Git("restore", "--staged", "--worktree", ".gitattributes")
	}
}
