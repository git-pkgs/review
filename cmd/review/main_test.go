package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/git-pkgs/review"
	"github.com/git-pkgs/review/internal/gittest"
)

func branchRepo(t *testing.T) *gittest.Repo {
	r := gittest.New(t)
	r.Write("lib/parse.go", "package lib\n\nfunc Parse(s string) int {\n\treturn len(s)\n}\n")
	r.Write("go.sum", "example.com/a v1.0.0 h1:x\n")
	r.Write("package-lock.json", "{}\n")
	r.Commit("base")
	r.Git("checkout", "-q", "-b", "topic")
	r.Write("lib/parse.go", "package lib\n\nfunc Parse(s string) int {\n\treturn len(s) + 1\n}\n")
	r.Write("lib/new.go", "package lib\n\nfunc New() {}\n")
	r.Write("package-lock.json", "{\"lockfileVersion\": 3}\n")
	r.Commit("topic")
	return r
}

func runCLI(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(stdin), &stdout, &stderr)
	return stdout.String(), err
}

func TestOverviewText(t *testing.T) {
	r := branchRepo(t)
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"topic\nmain...HEAD (",
		"), 3 files, +5 -2\n",
		"Read in this order\n\n1. Changes to lib/new.go and lib/parse.go  (2 files, +4 -1)\n" +
			"   Adds lib/new.go. Adds function `New`. Changes the body of function `Parse`. No test\n   files changed.\n" +
			"     A lib/new.go    +3 -0\n     M lib/parse.go  +1 -1\n",
		"Safe to skim\n\n2. Generated files  (1 file, +1 -1)\n   Generated (lockfile): package-lock.json.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
}

func TestFlagsAfterRange(t *testing.T) {
	r := branchRepo(t)
	out, err := runCLI(t, "", "overview", "main", "--json", "-C", r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var o review.Overview
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if o.Range == nil || o.Range.Base != "main" {
		t.Errorf("range = %+v", o.Range)
	}
}

func TestHelp(t *testing.T) {
	out, err := runCLI(t, "", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"overview", "show", "--patch", "-C, --dir"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q\n%s", want, out)
		}
	}
}

func TestOverviewVerbose(t *testing.T) {
	r := branchRepo(t)
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "-v", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "     A lib/new.go    +3 -0\n         + func New() {}\n     M lib/parse.go  +1 -1\n         declarations unchanged\n") {
		t.Errorf("verbose output:\n%s", out)
	}
}

func TestOverviewFormats(t *testing.T) {
	r := branchRepo(t)
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "--json", "main..topic")
	if err != nil {
		t.Fatal(err)
	}
	var o review.Overview
	if err := json.Unmarshal([]byte(out), &o); err != nil {
		t.Fatal(err)
	}
	if o.Range == nil || o.Range.MergeBase || o.Range.Head != "topic" || len(o.Files) != 3 || len(o.Groups) != 2 || len(o.Commits) != 0 {
		t.Errorf("overview = %+v", o)
	}
	md, err := runCLI(t, "", "overview", "-C", r.Dir, "--format", "markdown", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(md, "## Branch topic\n") || !strings.Contains(md, "**1. Changes to lib/new.go and lib/parse.go** (2 files, +4 -1)") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestOverviewPatch(t *testing.T) {
	r := branchRepo(t)
	patch := r.Git("diff", "main...topic") + "\n"

	fromStdin, err := runCLI(t, patch, "overview", "--patch", "-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "change.diff")
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	fromFile, err := runCLI(t, "", "overview", "--patch", path)
	if err != nil {
		t.Fatal(err)
	}
	if fromStdin != fromFile {
		t.Errorf("stdin and file output differ:\n%s\n---\n%s", fromStdin, fromFile)
	}
	if !strings.HasPrefix(fromStdin, "Changes\n3 files, +5 -2\n") || !strings.Contains(fromStdin, "Adds lib/new.go. Declares `func New() {}`.") {
		t.Errorf("unexpected patch output:\n%s", fromStdin)
	}
}

func TestShow(t *testing.T) {
	r := branchRepo(t)
	out, err := runCLI(t, "", "show", "-C", r.Dir, "1", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "# 1. Changes to lib/new.go and lib/parse.go\ndiff --git a/lib/new.go b/lib/new.go\n") || strings.Contains(out, "package-lock.json") {
		t.Errorf("show output:\n%s", out)
	}

	r.Git("mv", "lib/new.go", "lib/renamed.go")
	r.Write("lib/renamed.go", "package lib\n\nfunc New() {}\n\nfunc Renamed() {}\n")
	r.Commit("rename")
	out, err = runCLI(t, "", "show", "-C", r.Dir, "1", "HEAD~1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rename from lib/new.go\nrename to lib/renamed.go\n") {
		t.Errorf("show of a rename:\n%s", out)
	}

	patch := r.Git("diff", "main...topic") + "\n"
	out, err = runCLI(t, patch, "show", "--patch", "-", "2")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(patch, "diff --git a/package-lock.json")
	if start < 0 {
		t.Fatalf("fixture patch has no package-lock.json section:\n%s", patch)
	}
	want := "# 2. Generated files\n" + patch[start:]
	if out != want {
		t.Errorf("patch show =\n%q\nwant\n%q", out, want)
	}
	for name, args := range map[string][]string{
		"no group":      {"show", "-C", r.Dir},
		"not a number":  {"show", "-C", r.Dir, "one"},
		"missing group": {"show", "-C", r.Dir, "9", "main"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestErrors(t *testing.T) {
	r := branchRepo(t)
	for name, args := range map[string][]string{
		"unknown command":  {"nope"},
		"patch and range":  {"overview", "--patch", "-", "main"},
		"bad revision":     {"overview", "-C", r.Dir, "missing"},
		"too many revs":    {"overview", "-C", r.Dir, "a", "b", "c"},
		"missing patch":    {"overview", "--patch", filepath.Join(t.TempDir(), "none.diff")},
		"unknown format":   {"overview", "-C", r.Dir, "--format", "yaml", "main"},
		"not a repository": {"overview", "-C", t.TempDir(), "main"},
	} {
		if _, err := runCLI(t, "", args...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFindGroup(t *testing.T) {
	groups := []review.Group{
		{ID: 1, Title: "one"},
		{ID: 2, Title: "two", Children: []review.Group{{ID: 1, Title: "two a"}, {ID: 2, Title: "two b"}}},
	}
	for label, want := range map[string]string{"1": "one", "2": "two", "2.2": "two b"} {
		ids, err := parseGroupID(label)
		if err != nil {
			t.Fatal(err)
		}
		g, err := findGroup(groups, ids, label)
		if err != nil || g.Title != want {
			t.Errorf("findGroup(%s) = %q, %v", label, g.Title, err)
		}
	}
	for _, bad := range []string{"3", "1.1", "2.3"} {
		ids, _ := parseGroupID(bad)
		if _, err := findGroup(groups, ids, bad); err == nil {
			t.Errorf("findGroup(%s) found a group", bad)
		}
	}
	for _, bad := range []string{"x", "2.", "2.a"} {
		if _, err := parseGroupID(bad); err == nil {
			t.Errorf("parseGroupID(%q) accepted", bad)
		}
	}
}

func TestShowFromSubdirectory(t *testing.T) {
	r := gittest.New(t)
	r.Write("lib/a.go", "package lib\n\nfunc A() int { return 1 }\n")
	r.Write("pages/[id]/view.go", "package view\n")
	r.Write("pages/x/view.go", "package view\n")
	r.Commit("base")
	r.Write("lib/a.go", "package lib\n\nfunc A() int { return 2 }\n")
	r.Write("pages/[id]/view.go", "package view\n\nvar V = 1\n")
	r.Commit("change")
	out, err := runCLI(t, "", "show", "-C", filepath.Join(r.Dir, "lib"), "1", "HEAD~1..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "+func A() int { return 2 }") {
		t.Errorf("show from a subdirectory:\n%s", out)
	}
	out, err = runCLI(t, "", "show", "-C", filepath.Join(r.Dir, "lib"), "2", "HEAD~1..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "+var V = 1") || strings.Contains(out, "pages/x/view.go") {
		t.Errorf("a glob character in a path matched another file:\n%s", out)
	}
}

func TestShowPatchKeepsModes(t *testing.T) {
	r := gittest.New(t)
	r.Write("run.sh", "#!/bin/sh\necho hello\n")
	r.Commit("base")
	if err := os.Chmod(filepath.Join(r.Dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.Commit("make executable")
	patch := r.Git("diff", "HEAD~1..HEAD") + "\n"
	out, err := runCLI(t, patch, "show", "--patch", "-", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "old mode 100644\nnew mode 100755\n") {
		t.Errorf("mode change lost:\n%s", out)
	}
	overview, err := runCLI(t, patch, "overview", "--patch", "-")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(overview, "Makes run.sh executable.") {
		t.Errorf("overview doesn't mention the mode change:\n%s", overview)
	}
}

func TestShowPatchKeepsNoNewlineMarkers(t *testing.T) {
	r := gittest.New(t)
	r.Write("data.txt", "before")
	r.Commit("base")
	r.Write("data.txt", "after")
	r.Commit("change")
	patch := r.Git("diff", "HEAD~1..HEAD") + "\n"
	out, err := runCLI(t, patch, "show", "--patch", "-", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-before\n\\ No newline at end of file\n+after\n\\ No newline at end of file\n") {
		t.Errorf("show:\n%s", out)
	}
}

func TestTemplateExpressionsStayReviewable(t *testing.T) {
	r := gittest.New(t)
	for p, s := range map[string]string{"auth.js": "`${checkPermission(user)}`", "cache.js": "`${cache.get(key)}`", "queue.js": "`${queue.peek()}`"} {
		r.Write(p, "export function run() {\n  return "+s+";\n}\n")
	}
	r.Commit("base")
	for p, s := range map[string]string{"auth.js": "`${true}`", "cache.js": "`${cache.delete(key)}`", "queue.js": "`${queue.pop()}`"} {
		r.Write(p, "export function run() {\n  return "+s+";\n}\n")
	}
	r.Commit("change three operations")
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "HEAD~1..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Mechanical changes") {
		t.Errorf("template expressions collapsed:\n%s", out)
	}
}

func TestVersion(t *testing.T) {
	out, err := runCLI(t, "", "--version")
	if err != nil || !strings.HasPrefix(out, "review version ") {
		t.Errorf("version = %q, %v", out, err)
	}
	version = "1.2.3"
	t.Cleanup(func() { version = "" })
	if got := moduleVersion(); got != "1.2.3" {
		t.Errorf("moduleVersion = %q with ldflags set", got)
	}
}

func TestShowLargePatch(t *testing.T) {
	var patch strings.Builder
	patch.WriteString("diff --git a/data.txt b/data.txt\n--- a/data.txt\n+++ b/data.txt\n@@ -1 +1,20000 @@\n-old\n")
	for i := range 20000 {
		fmt.Fprintf(&patch, "+row %d %s\n", i, strings.Repeat("x", 60))
	}
	out, err := runCLI(t, patch.String(), "show", "--patch", "-", "1")
	if err != nil {
		t.Fatal(err)
	}
	if out != "# 1. Changes to data.txt\n"+patch.String() {
		t.Errorf("show changed a large patch: got %d bytes, want %d", len(out), len(patch.String())+len("# 1. Changes to data.txt\n"))
	}
}

func TestReadStdinStopsOnCancel(t *testing.T) {
	r, w := io.Pipe()
	defer func() { _ = w.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	src := &source{patch: "-"}
	done := make(chan error, 1)
	go func() {
		_, err := src.read(ctx, r)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read kept waiting after cancel")
	}
}

func TestInterruptWhileWaitingForPatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT can't be sent to a child process on Windows")
	}
	bin := filepath.Join(t.TempDir(), "review")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	// The first run of a new binary can be slow while the OS checks it, and a
	// signal sent before main installs its handler kills the process outright.
	if out, err := exec.Command(bin, "--version").CombinedOutput(); err != nil {
		t.Fatalf("warm up: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "overview", "--patch", "-")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if code := cmd.ProcessState.ExitCode(); code != exitInterrupted {
			t.Errorf("exit code = %d, want %d", code, exitInterrupted)
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("SIGINT didn't stop review while it waited for a patch on stdin")
	}
}

func TestInterruptWhileReadingRepository(t *testing.T) {
	r := branchRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{{"overview", "-C", r.Dir, "main"}, {"show", "1", "-C", r.Dir, "main"}} {
		err := run(ctx, args, strings.NewReader(""), io.Discard, io.Discard)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%v: err = %v, want context.Canceled", args, err)
		}
	}
}

func TestPatchPrefixes(t *testing.T) {
	r := gittest.New(t)
	r.Write("a/lib.go", "package a\n")
	r.Write("main.go", "package main\n")
	r.Commit("base")
	r.Write("a/lib.go", "package a\n\nfunc Lib() {}\n")
	r.Write("main.go", "package main\n\nfunc main() {}\n")
	r.Write("new.go", "package main\n")
	r.Git("add", "-N", "new.go")
	for _, config := range []string{"diff.mnemonicPrefix=false", "diff.mnemonicPrefix=true", "diff.noprefix=true"} {
		for _, rev := range []string{"--", "HEAD"} {
			patch := r.Git("-c", config, "diff", rev) + "\n"
			out, err := runCLI(t, patch, "overview", "--patch", "-", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var o review.Overview
			if err := json.Unmarshal([]byte(out), &o); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range o.Files {
				got = append(got, f.Path+" "+f.Status)
			}
			want := []string{"a/lib.go modified", "main.go modified", "new.go added"}
			if !slices.Equal(got, want) {
				t.Errorf("%s diff %s: files = %q, want %q", config, rev, got, want)
			}
		}
	}
}

func TestOneLineBodyEditIsNotAnAddition(t *testing.T) {
	r := gittest.New(t)
	r.Write("a.go", "package a\n\nfunc Value() int { return 1 }\n")
	r.Commit("base")
	r.Write("a.go", "package a\n\nfunc Value() int { return 2 }\n\nfunc Other() int { return 3 }\n")
	r.Commit("change value")
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "HEAD~1..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Adds function `Other`. Changes the body of function `Value`.") {
		t.Errorf("summary:\n%s", out)
	}
}

func TestLongFunctionSignatureChange(t *testing.T) {
	for name, parameter := range map[string]string{
		"ASCII limit":    strings.Repeat("x", 250),
		"UTF-8 boundary": strings.Repeat("x", 227) + "变量",
	} {
		t.Run(name, func(t *testing.T) {
			r := gittest.New(t)
			declaration := "func Value(" + parameter + " int) "
			r.Write("a.go", "package a\n\n"+declaration+"int { return 1 }\n")
			r.Commit("base")
			r.Write("a.go", "package a\n\n"+declaration+"int64 { return 1 }\n")
			r.Commit("change return type")
			out, err := runCLI(t, "", "overview", "-C", r.Dir, "HEAD~1..HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "Changes the declaration of function `Value`.") {
				t.Errorf("summary:\n%s", out)
			}
		})
	}
}

func TestNewMethodOnAnotherClassIsAnAddition(t *testing.T) {
	r := gittest.New(t)
	r.Write("a.py", "class A:\n    def value(self):\n        return 1\n\nclass B:\n    pass\n")
	r.Commit("base")
	r.Write("a.py", "class A:\n    def value(self):\n        return 1\n\nclass B:\n    def value(self):\n        return 2\n")
	r.Commit("add B.value")
	out, err := runCLI(t, "", "overview", "-C", r.Dir, "HEAD~1..HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Adds function `value`.") {
		t.Errorf("summary:\n%s", out)
	}
}
