package review

import (
	"cmp"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/git-pkgs/roles"
)

// Group kinds, in the order they are presented.
const (
	GroupChange       = "change"
	GroupDocs         = "docs"
	GroupConfig       = "config"
	GroupDependencies = "dependencies"
	GroupMechanical   = "mechanical"
	GroupSkim         = "skim"
)

// minParallelFiles is how many files must share an edit, apart from its
// string and number values, to be collapsed.
const minParallelFiles = 3

// minSameEdit is how many files must share an identical edit to be collapsed.
const minSameEdit = 2

// maxTitleFiles is how many paths a group title names before summarising.
const maxTitleFiles = 2

// Group is a set of files to read together. IDs follow presentation order and
// are stable for the same input.
type Group struct {
	ID        int      `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Summary   []string `json:"summary"`
	Commits   []string `json:"commits,omitempty"`
	Files     []string `json:"files"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	// Children split a large group into parts, numbered from 1 within it.
	Children []Group `json:"children,omitempty"`

	// rank breaks ties in reading order; lower reads first.
	rank int
}

// Group partitions the files into groups in reading order, replacing any
// earlier grouping.
func (o *Overview) Group() {
	g := grouper{o: o}
	buckets := map[string][]*File{}
	for i := range o.Files {
		f := &o.Files[i]
		k := category(f)
		buckets[k] = append(buckets[k], f)
	}

	changed, parallel := g.splitParallel(buckets[GroupChange])
	var groups []Group
	groups = append(groups, g.changeGroups(changed)...)
	if docs := buckets[GroupDocs]; len(docs) > 0 {
		groups = append(groups, g.roleGroup(GroupDocs, "Documentation", docs))
	}
	if cfg := buckets[GroupConfig]; len(cfg) > 0 {
		groups = append(groups, g.roleGroup(GroupConfig, "Build, CI and tooling", cfg))
	}
	if deps := buckets[GroupDependencies]; len(deps) > 0 {
		groups = append(groups, g.roleGroup(GroupDependencies, "Dependency manifests", deps))
	}
	groups = append(groups, g.mechanicalGroups(buckets[GroupMechanical], parallel)...)
	if skim := buckets[GroupSkim]; len(skim) > 0 {
		groups = append(groups, g.skimGroup(skim))
	}
	for i := range groups {
		groups[i].ID = i + 1
	}
	o.Groups = groups
}

type grouper struct {
	o *Overview
}

func has(f *File, role roles.Role) bool { return slices.Contains(f.Roles, role) }

func category(f *File) string {
	switch {
	case f.Skip != "":
		return GroupSkim
	case f.Mechanical != "":
		return GroupMechanical
	case has(f, roles.CI):
		return GroupConfig
	case has(f, roles.Test) || has(f, roles.Fixture) || has(f, roles.Example) || has(f, roles.Benchmark):
		return GroupChange
	case f.Dependency != nil:
		return GroupDependencies
	case has(f, roles.Source):
		return GroupChange
	case has(f, roles.Documentation) || has(f, roles.Legal):
		return GroupDocs
	case has(f, roles.Build) || has(f, roles.Configuration) || has(f, roles.Tooling) || has(f, roles.Packaging):
		return GroupConfig
	}
	return GroupChange
}

type parallelSet struct {
	title  string
	files  []*File
	values bool
}

// splitParallel takes out sets of files that received the same edit, or the
// same edit with different values, such as translations of new keys.
func (g *grouper) splitParallel(files []*File) ([]*File, []parallelSet) {
	taken := map[*File]bool{}
	var sets []parallelSet

	byEdit := map[string][]*File{}
	for _, f := range files {
		if f.fingerprint != "" && f.Status == Modified {
			byEdit[f.fingerprint] = append(byEdit[f.fingerprint], f)
		}
	}
	for _, fs := range sortedValues(byEdit) {
		if len(fs) < minSameEdit {
			continue
		}
		sets = append(sets, parallelSet{title: fmt.Sprintf("Same edit in %d files", len(fs)), files: fs})
		for _, f := range fs {
			taken[f] = true
		}
	}

	byValues := map[string][]*File{}
	for _, f := range files {
		if !taken[f] && f.literalFingerprint != "" && f.Status == Modified {
			byValues[f.literalFingerprint] = append(byValues[f.literalFingerprint], f)
		}
	}
	for _, fs := range sortedValues(byValues) {
		if len(fs) < minParallelFiles {
			continue
		}
		title := fmt.Sprintf("Same edit with different values in %d files", len(fs))
		if dir := commonDir(fs); dir != "." {
			title += " under " + dirLabel(dir)
		}
		sets = append(sets, parallelSet{title: title, files: fs, values: true})
		for _, f := range fs {
			taken[f] = true
		}
	}

	var rest []*File
	for _, f := range files {
		if !taken[f] {
			rest = append(rest, f)
		}
	}
	return rest, sets
}

func sortedValues(m map[string][]*File) [][]*File {
	out := make([][]*File, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b []*File) int { return strings.Compare(a[0].Path, b[0].Path) })
	return out
}

// changeGroups joins linked code and tests, then groups remaining files by directory.
func (g *grouper) changeGroups(files []*File) []Group {
	comps := g.byLinks(files)
	groups := make([]Group, 0, len(comps))
	fileGroup := map[string]int{}
	for _, comp := range comps {
		slices.SortFunc(comp, func(a, b *File) int { return strings.Compare(a.Path, b.Path) })
		grp := g.newGroup(GroupChange, contentTitle(comp), comp)
		grp.Summary = changeSummary(comp)
		grp = g.withParts(grp, comp)
		grp.rank = -(grp.Additions + grp.Deletions)
		for _, f := range comp {
			fileGroup[f.Path] = len(groups)
		}
		groups = append(groups, grp)
	}
	dedupeTitles(groups)
	return g.order(groups, fileGroup)
}

func (g *grouper) byLinks(files []*File) [][]*File {
	if len(files) == 0 {
		return nil
	}
	index := map[string]int{}
	for i, f := range files {
		index[f.Path] = i
	}
	uf := newUnionFind(len(files))
	linked := make([]bool, len(files))
	for _, l := range g.o.Links {
		a, okA := index[l.From]
		b, okB := index[l.To]
		if okA && okB {
			uf.union(a, b)
			linked[a], linked[b] = true, true
		}
	}
	for i, f := range files {
		if j, ok := testSubject(f, files); ok {
			uf.union(i, j)
			linked[i], linked[j] = true, true
		}
	}
	byDir := map[string]int{}
	for i, f := range files {
		if linked[i] {
			continue
		}
		dir := path.Dir(f.Path)
		if j, ok := byDir[dir]; ok {
			uf.union(i, j)
		} else {
			byDir[dir] = i
		}
	}
	components := map[int][]*File{}
	var roots []int
	for i, f := range files {
		root := uf.find(i)
		if _, ok := components[root]; !ok {
			roots = append(roots, root)
		}
		components[root] = append(components[root], f)
	}
	comps := make([][]*File, 0, len(roots))
	for _, r := range roots {
		comps = append(comps, components[r])
	}
	return comps
}

func (g *grouper) newGroup(kind, title string, files []*File) Group {
	grp := Group{Kind: kind, Title: title}
	for _, f := range files {
		grp.Files = append(grp.Files, f.Path)
		grp.Additions += f.Additions
		grp.Deletions += f.Deletions
	}
	return grp
}

// contentTitle names a group after its files.
func contentTitle(files []*File) string {
	var main []string
	tests := 0
	for _, f := range files {
		if has(f, roles.Test) {
			tests++
		} else {
			main = append(main, f.Path)
		}
	}
	suffix := ""
	if tests > 0 && len(main) > 0 {
		suffix = " and tests"
	}
	switch {
	case len(main) == 0:
		return testTitle(files)
	case len(main) > maxTitleFiles:
		if dir := commonDir(files); dir != "." {
			return "Changes in " + dirLabel(dir) + suffix
		}
		main = append(main[:maxTitleFiles], plural(len(main)-maxTitleFiles, "other file"))
	}
	return "Changes to " + joinAnd(main) + suffix
}

// testTitle names a group made only of tests or fixtures.
func testTitle(files []*File) string {
	word := "Fixtures"
	for _, f := range files {
		if !has(f, roles.Fixture) {
			word = "Tests"
		}
	}
	if len(files) == 1 {
		return word + ": " + files[0].Path
	}
	if dir := commonDir(files); dir != "." {
		return word + " in " + dirLabel(dir)
	}
	return word
}

func commonDir(files []*File) string {
	dir := path.Dir(files[0].Path)
	for _, f := range files[1:] {
		for dir != "." && !strings.HasPrefix(f.Path, dir+"/") {
			dir = path.Dir(dir)
		}
	}
	return dir
}

func dedupeTitles(groups []Group) {
	count := map[string]int{}
	for _, grp := range groups {
		count[grp.Title]++
	}
	for i := range groups {
		if count[groups[i].Title] <= 1 {
			continue
		}
		files := make([]*File, 0, len(groups[i].Files))
		for _, p := range groups[i].Files {
			files = append(files, &File{Path: p})
		}
		where := groups[i].Files[0]
		if len(files) > 1 {
			where = dirLabel(commonDir(files))
		}
		groups[i].Title += " (" + where + ")"
	}
}

// order puts groups whose code others use before the groups using it, then
// follows each group's rank.
func (g *grouper) order(groups []Group, fileGroup map[string]int) []Group {
	n := len(groups)
	deps := make([]map[int]bool, n)
	for i := range deps {
		deps[i] = map[int]bool{}
	}
	for _, l := range g.o.Links {
		from, okF := fileGroup[l.From]
		to, okT := fileGroup[l.To]
		if okF && okT && from != to {
			deps[from][to] = true
		}
	}
	done := make([]bool, n)
	var out []Group
	for len(out) < n {
		ready := readyGroups(deps, done, true)
		if len(ready) == 0 {
			// A cycle: fall back to rank among what's left.
			ready = readyGroups(deps, done, false)
		}
		next := slices.MinFunc(ready, func(a, b int) int {
			if c := cmp.Compare(groups[a].rank, groups[b].rank); c != 0 {
				return c
			}
			return strings.Compare(groups[a].Title, groups[b].Title)
		})
		done[next] = true
		out = append(out, groups[next])
	}
	return out
}

// readyGroups lists groups not yet placed, and with blocking set, only those
// whose dependencies are all placed.
func readyGroups(deps []map[int]bool, done []bool, blocking bool) []int {
	var ready []int
	for i := range deps {
		if done[i] {
			continue
		}
		blocked := false
		for d := range deps[i] {
			blocked = blocked || (blocking && !done[d])
		}
		if !blocked {
			ready = append(ready, i)
		}
	}
	return ready
}

func (g *grouper) roleGroup(kind, title string, files []*File) Group {
	grp := g.newGroup(kind, title, files)
	grp.Summary = roleSummary(files)
	return grp
}

func (g *grouper) mechanicalGroups(files []*File, parallel []parallelSet) []Group {
	var groups []Group
	byReason := map[string][]*File{}
	var reasons []string
	for _, f := range files {
		if _, ok := byReason[f.Mechanical]; !ok {
			reasons = append(reasons, f.Mechanical)
		}
		byReason[f.Mechanical] = append(byReason[f.Mechanical], f)
	}
	slices.Sort(reasons)
	for _, reason := range reasons {
		fs := byReason[reason]
		grp := g.newGroup(GroupMechanical, upperFirst(reason), fs)
		grp.Summary = mechanicalSummary(reason, fs)
		groups = append(groups, grp)
	}
	for _, set := range parallel {
		grp := g.newGroup(GroupMechanical, set.title, set.files)
		if set.values {
			grp.Summary = []string{"Each file gets the same lines apart from string and number values; reading one shows the pattern."}
		} else {
			grp.Summary = []string{"Each file gets exactly the same lines; reading one shows the pattern."}
		}
		groups = append(groups, grp)
	}
	return groups
}

func (g *grouper) skimGroup(files []*File) Group {
	byReason := map[string][]string{}
	var reasons, kinds []string
	for _, f := range files {
		if _, ok := byReason[f.Skip]; !ok {
			reasons = append(reasons, f.Skip)
		}
		byReason[f.Skip] = append(byReason[f.Skip], f.Path)
		kind, _, _ := strings.Cut(f.Skip, " ")
		if w := skimWords[kind]; !slices.Contains(kinds, w) {
			kinds = append(kinds, w)
		}
	}
	slices.Sort(reasons)
	slices.Sort(kinds)
	grp := g.newGroup(GroupSkim, upperFirst(joinAnd(kinds))+" files", files)
	for _, r := range reasons {
		grp.Summary = append(grp.Summary, upperFirst(r)+": "+nameList(byReason[r])+".")
	}
	return grp
}

var skimWords = map[string]string{
	"generated":    "generated",
	"vendor":       "vendored",
	"build-output": "build output",
	"minified":     "minified",
	"cache":        "cache",
	skipBinary:     "binary",
	skipSubmodule:  "submodule",
}

type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	uf := &unionFind{parent: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (u *unionFind) find(i int) int {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[max(ra, rb)] = min(ra, rb)
	}
}
