package gitdiff

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/git-pkgs/review/internal/gittest"
)

func TestParseRaw(t *testing.T) {
	oid1 := strings.Repeat("a", 40)
	oid2 := strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	out := ":100644 100644 " + oid1 + " " + oid2 + " M\x00a.go\x00" +
		":100644 100644 " + oid1 + " " + oid1 + " R100\x00old name.go\x00new name.go\x00" +
		":000000 100644 " + zero + " " + oid2 + " A\x00new.go\x00"
	changes, err := parseRaw([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("got %d changes", len(changes))
	}
	if c := changes[0]; c.status != 'M' || c.path != "a.go" || c.oldOID != oid1 || c.newOID != oid2 {
		t.Errorf("modified = %+v", c)
	}
	if c := changes[1]; c.status != 'R' || c.score != 100 || c.oldPath != "old name.go" || c.path != "new name.go" {
		t.Errorf("renamed = %+v", c)
	}
	if c := changes[2]; c.status != 'A' || c.oldMode != "000000" || c.path != "new.go" {
		t.Errorf("added = %+v", c)
	}
}

func TestParseRawRejectsGarbage(t *testing.T) {
	for _, out := range []string{"nonsense\x00", ":100644 100644 a b R100\x00only-one\x00"} {
		if _, err := parseRaw([]byte(out)); err == nil {
			t.Errorf("parseRaw(%q) accepted", out)
		}
	}
}

func TestParseNumstat(t *testing.T) {
	out := "3\t1\ta.go\x00-\t-\tlogo.png\x000\t0\t\x00old.go\x00new.go\x002\t0\tb.go\x00"
	counts, err := parseNumstat([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []lineCounts{{additions: 3, deletions: 1}, {binary: true}, {}, {additions: 2}}
	if len(counts) != len(want) {
		t.Fatalf("got %d counts, want %d", len(counts), len(want))
	}
	for i := range want {
		if counts[i] != want[i] {
			t.Errorf("counts[%d] = %+v, want %+v", i, counts[i], want[i])
		}
	}
}

func TestHunksByPath(t *testing.T) {
	r := gittest.New(t)
	r.Write("link", "contents\n")
	r.Write("data.txt", "before")
	r.Write("old.go", "package a\n\nfunc A() {}\n\nfunc C() {}\n\nfunc D() {}\n\nfunc E() {}\n")
	r.Commit("base")
	if err := os.Remove(filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(r.Dir, "link")); err != nil {
		t.Fatal(err)
	}
	r.Write("data.txt", "after")
	r.Git("mv", "old.go", "new.go")
	r.Write("new.go", "package a\n\nfunc B() {}\n\nfunc C() {}\n\nfunc D() {}\n\nfunc E() {}\n")
	r.Commit("change")
	byPath, err := gitRunner{dir: r.Dir}.hunks(context.Background(), "HEAD~1", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	// The type change arrives as two sections, a deletion and an addition.
	if link := byPath["link"]; len(link) != 2 || link[0].Lines[0] != "-contents" || link[1].Lines[0] != "+target" {
		t.Errorf("link hunks = %+v", link)
	}
	data := byPath["data.txt"]
	if len(data) != 1 || strings.Join(data[0].Lines, "|") != "-before|\\ No newline at end of file|+after|\\ No newline at end of file" {
		t.Errorf("data.txt hunks = %+v", data)
	}
	if len(byPath["new.go"]) != 1 || len(byPath["old.go"]) != 0 {
		t.Errorf("rename hunks = %+v", byPath)
	}
}
