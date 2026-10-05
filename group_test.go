package review

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func analyzed(t *testing.T, diffs ...FileDiff) *Overview {
	t.Helper()
	o := &Overview{}
	for _, d := range diffs {
		f, err := Analyze(d)
		if err != nil {
			t.Fatal(err)
		}
		o.Add(f)
	}
	return o
}

func mod(path string, add, del int) FileDiff {
	return FileDiff{Path: path, Status: Modified, Additions: add, Deletions: del}
}

func groupTitles(o *Overview) []string {
	var titles []string
	for _, g := range o.Groups {
		titles = append(titles, g.Title)
	}
	return titles
}

func groupOf(t *testing.T, o *Overview, path string) Group {
	t.Helper()
	for _, g := range o.Groups {
		if slices.Contains(g.Files, path) {
			return g
		}
	}
	t.Fatalf("%s is in no group", path)
	return Group{}
}

func TestGroupByCommit(t *testing.T) {
	o := analyzed(t,
		mod("src/parse.go", 20, 5),
		mod("src/parse_test.go", 30, 0),
		mod("src/serve.go", 10, 2),
		mod("README.md", 3, 1),
	)
	o.Commits = []Commit{
		{Subject: "Add strict parsing", Files: []CommitFile{{Path: "src/parse.go", Additions: 18, Deletions: 5}, {Path: "src/parse_test.go", Additions: 30}, {Path: "README.md", Additions: 3, Deletions: 1}}},
		{Subject: "Serve parsed input", Files: []CommitFile{{Path: "src/serve.go", Additions: 10, Deletions: 2}, {Path: "src/parse.go", Additions: 2}}},
	}
	o.Group()
	want := []string{"Add strict parsing", "Serve parsed input", "Documentation"}
	if got := groupTitles(o); !slices.Equal(got, want) {
		t.Fatalf("titles = %q, want %q", got, want)
	}
	strict := o.Groups[0]
	if !slices.Equal(strict.Files, []string{"src/parse.go", "src/parse_test.go"}) {
		t.Errorf("strict parsing files = %q", strict.Files)
	}
	if !slices.Contains(strict.Summary, "Documented in README.md.") {
		t.Errorf("summary = %q", strict.Summary)
	}
	for i, g := range o.Groups {
		if g.ID != i+1 {
			t.Errorf("group %d has id %d", i, g.ID)
		}
	}
}

func TestGroupByLinks(t *testing.T) {
	o := analyzed(t,
		mod("api/handler.go", 10, 1),
		mod("store/store.go", 40, 3),
		mod("store/store_test.go", 15, 0),
		mod("cli/main.go", 2, 2),
		mod("cli/flags.go", 1, 1),
	)
	o.Links = []Link{{From: "api/handler.go", To: "store/store.go"}}
	o.Group()
	if len(o.Groups) != 2 {
		t.Fatalf("groups = %+v", o.Groups)
	}
	linked := groupOf(t, o, "api/handler.go")
	if !slices.Equal(linked.Files, []string{"api/handler.go", "store/store.go", "store/store_test.go"}) {
		t.Errorf("linked files = %q", linked.Files)
	}
	if cli := groupOf(t, o, "cli/main.go"); !slices.Equal(cli.Files, []string{"cli/flags.go", "cli/main.go"}) || cli.Title != "Changes to cli/flags.go and cli/main.go" {
		t.Errorf("cli group = %+v", cli)
	}
}

func TestGroupOrderFollowsLinks(t *testing.T) {
	o := analyzed(t, mod("ui/view.go", 50, 0), mod("core/model.go", 5, 0))
	o.Commits = []Commit{
		{Subject: "Add view", Files: []CommitFile{{Path: "ui/view.go", Additions: 50}}},
		{Subject: "Add model field", Files: []CommitFile{{Path: "core/model.go", Additions: 5}}},
	}
	o.Links = []Link{{From: "ui/view.go", To: "core/model.go"}}
	o.Group()
	if got := groupTitles(o); !slices.Equal(got, []string{"Add model field", "Add view"}) {
		t.Errorf("titles = %q, want the used code first", got)
	}
}

func TestGroupCategories(t *testing.T) {
	o := analyzed(t,
		mod("src/app.go", 1, 1),
		mod("go.mod", 2, 1),
		mod("go.sum", 4, 2),
		mod("testdata/proj/package.json", 1, 0),
		mod(".github/workflows/ci.yml", 3, 0),
		mod("docs/guide.md", 5, 0),
		mod("vendor/x/x.go", 1, 1),
		FileDiff{Path: "old.go", OldPath: "older.go", Status: Renamed},
	)
	o.Group()
	kinds := map[string]string{}
	for _, g := range o.Groups {
		for _, f := range g.Files {
			kinds[f] = g.Kind
		}
	}
	want := map[string]string{
		"src/app.go":                 GroupChange,
		"testdata/proj/package.json": GroupChange,
		"go.mod":                     GroupDependencies,
		"go.sum":                     GroupSkim,
		".github/workflows/ci.yml":   GroupConfig,
		"docs/guide.md":              GroupDocs,
		"vendor/x/x.go":              GroupSkim,
		"old.go":                     GroupMechanical,
	}
	for path, kind := range want {
		if kinds[path] != kind {
			t.Errorf("%s in %q, want %q", path, kinds[path], kind)
		}
	}
	if skim := groupOf(t, o, "go.sum"); skim.Title != "Generated and vendored files" {
		t.Errorf("skim title = %q", skim.Title)
	}
}

func TestGroupParallelEdits(t *testing.T) {
	withLines := func(path string, lines ...string) FileDiff {
		d := mod(path, 1, 1)
		d.Hunks = []Hunk{{NewStart: 3, NewLines: 1, Lines: lines}}
		return d
	}
	diffs := []FileDiff{
		withLines("a/one.go", " a/one.go", "-oldName()", "+newName()"),
		withLines("b/two.go", " b/two.go", "-oldName()", "+newName()"),
	}
	for lang, word := range map[string]string{"de": "Neu", "en": "New", "fr": "Nouveau"} {
		diffs = append(diffs, withLines("locales/"+lang+".json", `+  "cta": "`+word+`",`))
	}
	diffs = append(diffs, withLines("locales/ja.json", `+  "other": "新"`))
	o := analyzed(t, diffs...)
	o.Group()

	edit := groupOf(t, o, "a/one.go")
	if edit.Kind != GroupMechanical || edit.Title != "Same edit in 2 files" || !slices.Contains(edit.Files, "b/two.go") {
		t.Errorf("same edit group = %+v", edit)
	}
	values := groupOf(t, o, "locales/de.json")
	if values.Kind != GroupMechanical || len(values.Files) != 3 || values.Title != "Same edit with different values in 3 files under locales/" {
		t.Errorf("same values group = %+v", values)
	}
	if groupOf(t, o, "locales/ja.json").Kind != GroupChange {
		t.Error("a file adding a different key joined the set")
	}
}

func TestGroupKeepsUnrelatedSameSizeEdits(t *testing.T) {
	o := analyzed(t,
		FileDiff{Path: "auth.go", Status: Modified, Additions: 1, Deletions: 1,
			Hunks: []Hunk{{Lines: []string{"-func Authorized() bool { return false }", "+func Authorized() bool { return true }"}}}},
		FileDiff{Path: "cache.go", Status: Modified, Additions: 1, Deletions: 1,
			Hunks: []Hunk{{Lines: []string{"-const TTL = 30", "+const TTL = 300"}}}},
		FileDiff{Path: "retry.go", Status: Modified, Additions: 1, Deletions: 1,
			Hunks: []Hunk{{Lines: []string{"-const Retries = 3", "+const Retries = 0"}}}},
	)
	o.Group()
	for _, g := range o.Groups {
		if g.Kind == GroupMechanical {
			t.Errorf("unrelated edits collapsed: %+v", g)
		}
	}
}

func TestGroupSummaryKeepsRemovals(t *testing.T) {
	// Removed() was deleted and Kept() edited; symbols only describe head.
	d := FileDiff{
		Path: "api.go", Status: Modified, Additions: 1, Deletions: 3,
		Base:    &Content{Data: []byte("package api\n\nfunc Removed() {}\n\nfunc Kept() int {\n\treturn 1\n}\n"), Complete: true},
		Head:    &Content{Data: []byte("package api\n\nfunc Kept() int {\n\treturn 2\n}\n"), Complete: true},
		Hunks:   []Hunk{{NewStart: 3, NewLines: 0, Lines: []string{"-func Removed() {}", "-"}}, {NewStart: 4, NewLines: 1, Lines: []string{"-\treturn 1", "+\treturn 2"}}},
		Symbols: []Symbol{{Name: "Kept", Kind: "func", StartLine: 3, EndLine: 5, Exported: true}},
	}
	o := analyzed(t, d)
	o.Group()
	got := strings.Join(o.Groups[0].Summary, " ")
	if !strings.Contains(got, "Changes the body of function `Kept`.") || !strings.Contains(got, "Removes `func Removed() {}`.") {
		t.Errorf("summary = %q", got)
	}
}

func TestModeSentences(t *testing.T) {
	o := analyzed(t,
		FileDiff{Path: "run.sh", Status: Modified, OldMode: "100644", NewMode: "100755"},
		FileDiff{Path: "old.sh", Status: Modified, OldMode: "100755", NewMode: "100644"},
		FileDiff{Path: "link", Status: TypeChanged, OldMode: "100644", NewMode: "120000"},
	)
	files := make([]*File, len(o.Files))
	for i := range o.Files {
		files[i] = &o.Files[i]
	}
	want := []string{"Makes run.sh executable.", "Makes old.sh no longer executable.", "Replaces link with a symbolic link."}
	if got := modeSentences(files); !slices.Equal(got, want) {
		t.Errorf("modeSentences = %q", got)
	}
}

func TestGroupTestSubject(t *testing.T) {
	o := analyzed(t,
		mod("lib/parser.rb", 5, 1),
		mod("spec/parser_spec.rb", 9, 0),
		mod("web/app.ts", 3, 1),
		mod("web/app.test.ts", 4, 0),
	)
	o.Group()
	if g := groupOf(t, o, "spec/parser_spec.rb"); !slices.Contains(g.Files, "lib/parser.rb") {
		t.Errorf("spec not grouped with its subject: %+v", g)
	}
	if g := groupOf(t, o, "web/app.test.ts"); !slices.Contains(g.Files, "web/app.ts") {
		t.Errorf("test not grouped with its subject: %+v", g)
	}
}

func TestGroupSummarySentences(t *testing.T) {
	d := FileDiff{
		Path: "src/parse.go", Status: Modified, Additions: 3, Deletions: 1,
		Hunks: []Hunk{
			{NewStart: 3, NewLines: 1, Lines: []string{"-func Parse() {", "+func Parse(strict bool) {"}},
			{NewStart: 11, NewLines: 2, Lines: []string{"+func New() {", "+}"}},
		},
		Symbols: []Symbol{
			{Name: "Parse", Kind: "func", StartLine: 3, EndLine: 5, Exported: true},
			{Name: "New", Kind: "func", StartLine: 11, EndLine: 12, Exported: true},
			{Name: "Options", Kind: "type", StartLine: 14, EndLine: 16, Exported: true},
		},
	}
	o := analyzed(t, d, FileDiff{Path: "src/new.go", Status: Added, Additions: 3,
		Head: &Content{Data: []byte("package p\n\nfunc Fresh() {}\n"), Complete: true}})
	o.Group()
	got := strings.Join(o.Groups[0].Summary, " ")
	want := "Adds src/new.go. Adds function `New`. Changes the declaration of function `Parse`. " +
		"Declares `func Fresh() {}`. No test files changed."
	if got != want {
		t.Errorf("summary =\n%s\nwant\n%s", got, want)
	}
}

func TestSymbolList(t *testing.T) {
	syms := []SymbolChange{
		{Name: "helper", Kind: "func"},
		{Name: "Config", Kind: "type", Exported: true},
		{Name: "Run", Kind: "func", Exported: true},
		{Name: "Run", Kind: "func", Exported: true},
	}
	if got := symbolList(syms); got != "type `Config`, and functions `Run` and `helper`" {
		t.Errorf("symbolList = %q", got)
	}
	var many []SymbolChange
	for _, n := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		many = append(many, SymbolChange{Name: n, Kind: "const", Exported: true})
	}
	if got := symbolList(many); !strings.HasSuffix(got, "(and 2 more)") || !strings.HasPrefix(got, "constants `A`") {
		t.Errorf("symbolList = %q", got)
	}
}

func TestRender(t *testing.T) {
	o := analyzed(t, mod("src/parse.go", 20, 5), mod("go.sum", 1, 1))
	o.Range = &Range{Base: "main", Head: "HEAD", BaseRev: strings.Repeat("a", 40), HeadRev: strings.Repeat("b", 40), MergeBase: true}
	o.Branch = "strict"
	o.Commits = []Commit{
		{SHA: strings.Repeat("1", 40), Subject: "Add strict parsing", Files: []CommitFile{{Path: "src/parse.go", Additions: 15}}},
		{SHA: strings.Repeat("2", 40), Subject: "Tidy parser", Files: []CommitFile{{Path: "src/parse.go", Additions: 5, Deletions: 5}}},
	}
	o.Group()

	var text bytes.Buffer
	if err := o.Render(&text, TextOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Branch strict\nmain...HEAD (aaaaaaaaaaaa..bbbbbbbbbbbb), 2 commits, 2 files, +21 -6\n",
		"Commits\n\n  1111111 Add strict parsing\n  2222222 Tidy parser\n",
		"Read in this order\n\n1. Add strict parsing  (1 file, +20 -5)\n",
		"     M src/parse.go  +20 -5\n",
		"Safe to skim\n\n2. Generated files  (1 file, +1 -1)\n   Generated (lockfile supplement): go.sum.\n",
		"review show <number>",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text missing %q\n%s", want, text.String())
		}
	}

	var md bytes.Buffer
	if err := o.Render(&md, TextOptions{Markdown: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Branch strict\n\n",
		"### Commits\n\n- Add strict parsing `1111111`\n",
		"### Read in this order\n\n**1. Add strict parsing** (1 file, +20 -5)\n",
		"- `src/parse.go` M +20 -5\n",
	} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown missing %q\n%s", want, md.String())
		}
	}
	if strings.Contains(md.String(), "review show") {
		t.Error("markdown includes CLI hints")
	}
}

func TestContentTitles(t *testing.T) {
	tests := []struct {
		paths []string
		want  string
	}{
		{[]string{"cli/main.go"}, "Changes to cli/main.go"},
		{[]string{"cli/main.go", "cli/flags.go"}, "Changes to cli/main.go and cli/flags.go"},
		{[]string{"cli/a.go", "cli/b.go", "cli/c.go"}, "Changes in cli/"},
		{[]string{"a.go", "b.go", "c.go"}, "Changes to a.go, b.go and 1 other file"},
		{[]string{"cli/a.go", "cli/a_test.go"}, "Changes to cli/a.go and tests"},
		{[]string{"testdata/x/a.yaml", "testdata/y/b.yaml"}, "Fixtures in testdata/"},
		{[]string{"cli/a_test.go", "cli/testdata/x.json"}, "Tests in cli/"},
		{[]string{"cli/a_test.go"}, "Tests: cli/a_test.go"},
	}
	for _, tt := range tests {
		o := analyzed(t, modAll(tt.paths)...)
		files := make([]*File, len(o.Files))
		for i := range o.Files {
			files[i] = &o.Files[i]
		}
		if got := contentTitle(files); got != tt.want {
			t.Errorf("contentTitle(%q) = %q, want %q", tt.paths, got, tt.want)
		}
	}
}
