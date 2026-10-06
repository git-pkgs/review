package review

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func readAll(t *testing.T, patch string) []FileDiff {
	t.Helper()
	var files []FileDiff
	_, err := ReadPatch(strings.NewReader(patch), func(d FileDiff) error {
		files = append(files, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

const gitPatch = `diff --git a/parser.go b/parser.go
index 1111111..2222222 100644
--- a/parser.go
+++ b/parser.go
@@ -1,3 +1,3 @@ package p
-func Parse(s string) int {
+func Parse(s string, strict bool) int {
 	return len(s)
 }
@@ -10,2 +10,2 @@ func Other() {
---- a/looks/like/a/header
-+++ b/neither
+--- still body
+diff --git a/x b/x
diff --git a/added.go b/added.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/added.go
@@ -0,0 +1,3 @@
+package p
+
+func Added() {}
\ No newline at end of file
diff --git a/gone.go b/gone.go
deleted file mode 100644
index 4444444..0000000
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package p
-func Gone() {}
diff --git a/old name.go b/new name.go
similarity index 100%
rename from old name.go
rename to new name.go
diff --git a/run.sh b/run.sh
old mode 100644
new mode 100755
diff --git a/link b/link
old mode 100644
new mode 120000
index 5555555..6666666
--- a/link
+++ b/link
@@ -1 +1 @@
-text
+target
\ No newline at end of file
diff --git a/logo.png b/logo.png
index 7777777..8888888 100644
Binary files a/logo.png and b/logo.png differ
diff --git "a/tab\there.go" "b/tab\there.go"
index 9999999..aaaaaaa 100644
--- "a/tab\there.go"
+++ "b/tab\there.go"
@@ -1 +1 @@
-package a
+package b
diff --git a/sub b/sub
index bbbbbbb..ccccccc 160000
--- a/sub
+++ b/sub
@@ -1 +1 @@
-Subproject commit bbbbbbb
+Subproject commit ccccccc
`

func TestReadPatchGit(t *testing.T) {
	files := readAll(t, gitPatch)
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	wantPaths := []string{"parser.go", "added.go", "gone.go", "new name.go", "run.sh", "link", "logo.png", "tab\there.go", "sub"}
	if !slices.Equal(paths, wantPaths) {
		t.Fatalf("paths = %q, want %q", paths, wantPaths)
	}

	parser := files[0]
	if parser.Status != Modified || parser.Additions != 3 || parser.Deletions != 3 || len(parser.Hunks) != 2 {
		t.Errorf("parser.go = %+v", parser)
	}
	if parser.Base != nil || parser.Head != nil {
		t.Error("modified file carries content it cannot know")
	}

	added := files[1]
	if added.Status != Added || added.Head == nil || !added.Head.Complete {
		t.Fatalf("added.go = %+v", added)
	}
	if got := string(added.Head.Data); got != "package p\n\nfunc Added() {}" {
		t.Errorf("added.go contents = %q", got)
	}

	gone := files[2]
	if gone.Status != Deleted || gone.Base == nil || string(gone.Base.Data) != "package p\nfunc Gone() {}\n" {
		t.Errorf("gone.go = %+v", gone)
	}

	renamed := files[3]
	if renamed.Status != Renamed || renamed.OldPath != "old name.go" || renamed.Similarity != 100 {
		t.Errorf("rename = %+v", renamed)
	}
	if files[4].Status != Modified {
		t.Errorf("mode change status = %q", files[4].Status)
	}
	if link := files[5]; link.Status != TypeChanged || !link.Symlink {
		t.Errorf("link = %+v", link)
	}
	if !files[6].Binary {
		t.Error("logo.png not binary")
	}
	if !files[8].Submodule {
		t.Error("sub not a submodule")
	}
}

func TestReadPatchPlainUnified(t *testing.T) {
	patch := "--- a.go\t2026-10-01 10:00:00.000000000 +0000\n" +
		"+++ a.go\t2026-10-02 10:00:00.000000000 +0000\n" +
		"@@ -1 +1,2 @@\n" +
		" package p\n" +
		"+func A() {}\n" +
		"--- /dev/null\t1970-01-01 00:00:00.000000000 +0000\n" +
		"+++ dir/b.go\t2026-10-02 10:00:00.000000000 +0000\n" +
		"@@ -0,0 +1 @@\n" +
		"+package p\n"
	files := readAll(t, patch)
	if len(files) != 2 {
		t.Fatalf("got %d files", len(files))
	}
	if f := files[0]; f.Path != "a.go" || f.Status != Modified || f.Additions != 1 {
		t.Errorf("a.go = %+v", f)
	}
	if f := files[1]; f.Path != "dir/b.go" || f.Status != Added || f.Head == nil || string(f.Head.Data) != "package p\n" {
		t.Errorf("b.go = %+v", f)
	}
}

func TestReadPatchFormatPatch(t *testing.T) {
	patch := `From 0123456789abcdef Mon Sep 17 00:00:00 2001
From: Someone <someone@example.com>
Subject: [PATCH] Change a

--- a/looks like a header but is the message
---
 a.go | 2 +-
 1 file changed, 1 insertion(+), 1 deletion(-)

diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-package a
+package b
--
2.47.0
`
	files := readAll(t, patch)
	if len(files) != 1 || files[0].Path != "a.go" || files[0].Additions != 1 || files[0].Deletions != 1 {
		t.Errorf("files = %+v", files)
	}
}

func TestReadPatchErrors(t *testing.T) {
	for name, patch := range map[string]string{
		"short hunk":   "--- a/a\n+++ b/a\n@@ -1,3 +1,3 @@\n a\n",
		"bad header":   "--- a/a\n+++ b/a\n@@ -x +1 @@\n",
		"bad line":     "--- a/a\n+++ b/a\n@@ -1 +1 @@\n?what\n",
		"no new count": "--- a/a\n+++ b/a\n@@ -1\n",
	} {
		_, err := ReadPatch(strings.NewReader(patch), func(FileDiff) error { return nil })
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestFromPatch(t *testing.T) {
	o, err := FromPatch(strings.NewReader(gitPatch + "diff --git a/pnpm-lock.yaml b/pnpm-lock.yaml\n--- a/pnpm-lock.yaml\n+++ b/pnpm-lock.yaml\n@@ -1 +1 @@\n-a: 1\n+a: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if o.Range != nil {
		t.Error("patch overview has a range")
	}
	byPath := map[string]File{}
	for _, f := range o.Files {
		byPath[f.Path] = f
	}
	if f := byPath["added.go"]; f.Surface == nil || !slices.Contains(f.Surface.Lines, SurfaceLine{Op: "+", Text: "func Added() {}"}) {
		t.Errorf("added.go surface = %+v", f.Surface)
	}
	if f := byPath["parser.go"]; f.Surface != nil {
		t.Errorf("parser.go surface without contents = %+v", f.Surface)
	}
	if f := byPath["pnpm-lock.yaml"]; f.Skip != "generated (lockfile)" {
		t.Errorf("pnpm-lock.yaml skip = %q", f.Skip)
	}
	if f := byPath["sub"]; f.Skip != "submodule" {
		t.Errorf("sub skip = %q", f.Skip)
	}

	var buf bytes.Buffer
	if err := o.Text(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Changes\n10 files, +10 -9\n",
		"Adds added.go. Deletes gone.go. Makes run.sh executable. Replaces link with a\n" +
			"   symbolic link. Declares `func Added() {}`. Removes `func Gone() {}`.",
		"     A added.go     +3 -0\n",
		"Mechanical changes\n\n2. Renamed without changes  (1 file)\n   Contents are identical: old name.go to new name.go.\n",
		"Safe to skim\n\n3. Binary, generated and submodule files  (3 files, +2 -2)\n" +
			"   Binary: logo.png. Generated (lockfile): pnpm-lock.yaml. Submodule: sub.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q\n%s", want, out)
		}
	}
}

func TestAnalyzeIdenticalContents(t *testing.T) {
	src := &Content{Data: []byte("package p\n\nfunc A() {}\n"), Complete: true}
	f, err := Analyze(FileDiff{Path: "b.go", OldPath: "a.go", Status: Renamed, Base: src, Head: src})
	if err != nil {
		t.Fatal(err)
	}
	if f.Surface != nil {
		t.Errorf("surface for unchanged contents = %+v", f.Surface)
	}
}

func TestAnalyzeIncompleteContents(t *testing.T) {
	f, err := Analyze(FileDiff{
		Path:   "a.go",
		Status: Modified,
		Base:   &Content{Data: []byte("package p\n"), Complete: true},
		Head:   &Content{Data: []byte("package p\n\nfunc A"), Complete: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.Surface != nil {
		t.Errorf("surface from a prefix = %+v", f.Surface)
	}
}

func TestReadPatchSeries(t *testing.T) {
	patch := `From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001
From: Someone <someone@example.com>
Subject: [PATCH 1/2] Add strict parsing to the
 parser

Strict mode rejects trailing input.
---
 a.go | 2 +-

diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1 +1 @@
-a
+b
-- 
2.47.0

From 2222222222222222222222222222222222222222 Mon Sep 17 00:00:00 2001
From: Someone <someone@example.com>
Subject: [PATCH 2/2] Document strict mode

---
diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1 +1,2 @@
 b
+c
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-x
+y
`
	if _, err := FromPatch(strings.NewReader(patch)); err == nil || !strings.Contains(err.Error(), "git diff base...HEAD") {
		t.Fatalf("series error = %v", err)
	}
	if _, err := ReadPatch(strings.NewReader(patch), func(FileDiff) error { return nil }); err == nil {
		t.Fatal("ReadPatch accepted a series")
	}
	var buf bytes.Buffer
	if err := WritePatchFiles(&buf, strings.NewReader(patch), []string{"a.go"}); err == nil || buf.Len() != 0 {
		t.Fatalf("WritePatchFiles: error %v, output %q", err, buf.String())
	}
}

func TestReadPatchSections(t *testing.T) {
	series := "From 1111111111111111111111111111111111111111 Mon Sep 17 00:00:00 2001\n" +
		"Subject: [PATCH] Change\n\n---\n a.go | 2 +-\n\n"
	crlf := "diff --git a/w.txt b/w.txt\n--- a/w.txt\n+++ b/w.txt\n@@ -1 +1 @@\n-a\r\n+b\r\n"
	input := series + gitPatch + crlf + "-- \n2.47.0\n"
	var sections []string
	var paths []string
	_, err := ReadPatchSections(strings.NewReader(input), func(d FileDiff, section string) error {
		paths = append(paths, d.Path)
		sections = append(sections, section)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sections, ""); got != gitPatch+crlf {
		t.Errorf("sections don't reproduce the diff:\n%q\nwant\n%q", got, gitPatch+crlf)
	}
	if !strings.HasPrefix(sections[0], "diff --git a/parser.go") || !strings.HasSuffix(sections[0], "+diff --git a/x b/x\n") {
		t.Errorf("first section = %q", sections[0])
	}
	if last := sections[len(sections)-1]; !strings.Contains(last, "-a\r\n+b\r\n") {
		t.Errorf("CRLF lost: %q", last)
	}
	if paths[len(paths)-1] != "w.txt" {
		t.Errorf("paths = %q", paths)
	}
}

func TestReadPatchSectionsPlainDiff(t *testing.T) {
	input := "Only in a: x\n--- a.go\t2026-10-01\n+++ a.go\t2026-10-02\n@@ -1 +1 @@\n-a\n+b\n"
	var got []string
	if _, err := ReadPatchSections(strings.NewReader(input), func(_ FileDiff, section string) error {
		got = append(got, section)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != input[len("Only in a: x\n"):] {
		t.Errorf("sections = %q", got)
	}
}
