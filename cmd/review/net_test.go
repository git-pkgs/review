package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/git-pkgs/review"
	"github.com/git-pkgs/review/internal/gittest"
)

func TestOverviewDependsOnEndpoints(t *testing.T) {
	r := branchRepo(t)
	r.Write("lib/parse_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestParse(t *testing.T) {\n\tif Parse(\"abc\") != 4 { t.Fatal(\"parse\") }\n}\n")
	r.Commit("test parsing separately")
	r.Git("mv", "lib/new.go", "lib/renamed.go")
	r.Commit("move new file")
	squashed := r.Git("commit-tree", "HEAD^{tree}", "-p", "main", "-m", "same end tree")
	var want review.Overview
	var shown string
	for i, head := range []string{"HEAD", squashed} {
		out, err := runCLI(t, "", "overview", "-C", r.Dir, "main.."+head, "--json")
		if err != nil {
			t.Fatal(err)
		}
		var o review.Overview
		if err := json.Unmarshal([]byte(out), &o); err != nil {
			t.Fatal(err)
		}
		o.Range, o.Branch = nil, ""
		if i == 0 {
			want = o
		} else if !reflect.DeepEqual(o, want) {
			t.Fatalf("history changed the overview:\n%+v\n%+v", want, o)
		}
		for _, g := range o.Groups {
			if slices.Contains(g.Files, "lib/parse_test.go") && !slices.Contains(g.Files, "lib/parse.go") {
				t.Fatalf("test separated from source: %+v", g)
			}
		}
		out, err = runCLI(t, "", "show", "1", "-C", r.Dir, "main.."+head)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			shown = out
		} else if out != shown {
			t.Fatalf("history changed show:\n%s\n%s", shown, out)
		}
	}
}

func TestPatchSeriesRejected(t *testing.T) {
	r := gittest.New(t)
	r.Write("a.go", "package a\n")
	r.Write("logo.png", "\x89PNG\r\n\x1a\n\x00one")
	r.Commit("base")
	r.Write("logo.png", "\x89PNG\r\n\x1a\n\x00two and more bytes")
	r.Commit("change logo")
	r.Git("mv", "a.go", "b.go")
	r.Commit("rename a")
	for _, flags := range [][]string{{}, {"--cover-letter"}} {
		args := append([]string{"format-patch", "--stdout", "--binary"}, flags...)
		patch := r.Git(append(args, "HEAD~2..HEAD")...) + "\n"
		for _, command := range [][]string{{"overview"}, {"show", "1"}} {
			out, err := runCLI(t, patch, append(command, "--patch", "-")...)
			if err == nil || !strings.Contains(err.Error(), "git diff base...HEAD") || out != "" {
				t.Fatalf("%v: output %q, error %v", command, out, err)
			}
		}
	}
	patch := r.Git("diff", "HEAD~2..HEAD") + "\n"
	if _, err := runCLI(t, patch, "overview", "--patch", "-"); err != nil {
		t.Fatalf("net diff rejected: %v", err)
	}
	if out, err := runCLI(t, patch+patch, "overview", "--patch", "-"); err == nil || !strings.Contains(err.Error(), "git diff base...HEAD") || out != "" {
		t.Fatalf("repeated file sections: output %q, error %v", out, err)
	}
}

func TestNetPatchTypeChanges(t *testing.T) {
	r := gittest.New(t)
	r.Write("a.go", "package a\n")
	r.Write("b.go", "package b\n")
	r.Commit("base")
	r.Git("rm", "a.go")
	if err := os.Symlink("b.go", filepath.Join(r.Dir, "a.go")); err != nil {
		t.Fatal(err)
	}
	r.Commit("use symlink")
	for _, rng := range []string{"HEAD~1..HEAD", "HEAD..HEAD~1"} {
		oldMode, newMode := "100644", "120000"
		if rng == "HEAD..HEAD~1" {
			oldMode, newMode = newMode, oldMode
		}
		patch := r.Git("diff", rng) + "\n"
		for _, args := range [][]string{{"overview", "-C", r.Dir, rng}, {"overview", "--patch", "-"}} {
			out, err := runCLI(t, patch, append(args, "--json")...)
			if err != nil {
				t.Fatal(err)
			}
			var o review.Overview
			if err := json.Unmarshal([]byte(out), &o); err != nil {
				t.Fatal(err)
			}
			if len(o.Files) != 1 || o.Files[0].Status != review.TypeChanged || o.Files[0].OldMode != oldMode || o.Files[0].NewMode != newMode {
				t.Fatalf("%v: files %+v", args, o.Files)
			}
		}
		shown, err := runCLI(t, patch, "show", "1", "--patch", "-")
		if err != nil || !strings.HasSuffix(shown, patch) {
			t.Fatalf("type change sections: %v\n%s", err, shown)
		}
	}
}

func TestSingleFormatPatch(t *testing.T) {
	r := branchRepo(t)
	patch := r.Git("format-patch", "-1", "--stdout") + "\n"
	net := r.Git("diff", "HEAD~1..HEAD") + "\n"
	for _, args := range [][]string{{"overview", "--json"}, {"show", "1"}} {
		args = append(args, "--patch", "-")
		got, err := runCLI(t, patch, args...)
		if err != nil {
			t.Fatal(err)
		}
		want, err := runCLI(t, net, args...)
		if err != nil || got != want {
			t.Fatalf("%v: error %v\nformat-patch:\n%s\nnet:\n%s", args, err, got, want)
		}
	}
}
