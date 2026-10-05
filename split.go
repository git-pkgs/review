package review

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/git-pkgs/roles"
)

const (
	// maxGroupFiles is the size above which a group is split into parts.
	maxGroupFiles = 12
	// minPartFiles is the smallest directory that becomes a part of its own;
	// smaller ones go into a part for the remaining files.
	minPartFiles = 3
	// minParts is how many sizeable directories a level needs to split at.
	minParts = 2
)

var testAffixes = []string{"_test", ".test", ".spec", "_spec", "-test", "-spec", "Test", "Tests", "Spec"}

// testDirs are directory names that hold tests rather than say where the
// tested code lives.
var testDirs = map[string]bool{"test": true, "tests": true, "spec": true, "specs": true, "__tests__": true, "testing": true}

// testSubject finds the file a test file is named after: foo_test.go,
// foo.test.ts, test_foo.py, FooTest.java and foo_spec.rb all name foo. When
// several files share that name, the one whose directories best match the
// test's, ignoring test directories, wins; a tie names none of them.
func testSubject(f *File, files []*File) (int, bool) {
	if !has(f, roles.Test) {
		return 0, false
	}
	stem := strings.TrimSuffix(path.Base(f.Path), path.Ext(f.Path))
	subject := strings.TrimPrefix(stem, "test_")
	for _, affix := range testAffixes {
		subject = strings.TrimSuffix(subject, affix)
	}
	if subject == stem || subject == "" {
		return 0, false
	}
	testDir := dirTokens(path.Dir(f.Path))
	best, bestScore, tied := -1, -1, false
	for i, other := range files {
		if other == f || has(other, roles.Test) || strings.TrimSuffix(path.Base(other.Path), path.Ext(other.Path)) != subject {
			continue
		}
		score := commonSuffix(testDir, dirTokens(path.Dir(other.Path)))
		if path.Dir(other.Path) == path.Dir(f.Path) {
			score = len(testDir) + 1
		}
		switch {
		case score > bestScore:
			best, bestScore, tied = i, score, false
		case score == bestScore:
			tied = true
		}
	}
	if best < 0 || tied {
		return 0, false
	}
	return best, true
}

func dirTokens(dir string) []string {
	var out []string
	for _, part := range strings.Split(dir, "/") {
		if part != "." && !testDirs[part] {
			out = append(out, part)
		}
	}
	return out
}

func commonSuffix(a, b []string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

// splitParts divides a large group by directory. It descends from the root
// while one directory holds most of the files, and splits at the first level
// with two or more directories of at least minPartFiles files. Smaller
// directories and files above that level form a final part. Tests join the
// part of the file they're named after. It returns no parts when no level
// gives a useful split.
func splitParts(files []*File) (parts [][]*File, dirs []string, rest []*File) {
	if len(files) <= maxGroupFiles {
		return nil, nil, nil
	}
	subject := map[*File]*File{}
	var anchors []*File
	for _, f := range files {
		if i, ok := testSubject(f, files); ok {
			subject[f] = files[i]
		} else {
			anchors = append(anchors, f)
		}
	}

	prefix := ""
	for {
		buckets := map[string][]*File{}
		for _, f := range anchors {
			if key, ok := nextDir(f.Path, prefix); ok {
				buckets[key] = append(buckets[key], f)
			}
		}
		var big []string
		for key, fs := range buckets {
			if len(fs) >= minPartFiles {
				big = append(big, key)
			}
		}
		if len(big) >= minParts {
			parts, rest := assembleParts(files, anchors, subject, prefix, big)
			for _, key := range big {
				dirs = append(dirs, path.Join(prefix, key))
			}
			return parts, dirs, rest
		}
		if len(big) == 1 && 2*len(buckets[big[0]]) > len(anchors) {
			prefix = path.Join(prefix, big[0])
			continue
		}
		return nil, nil, nil
	}
}

// nextDir names the directory under prefix that holds p, or false when p is
// not below prefix or sits directly in it.
func nextDir(p, prefix string) (string, bool) {
	rest := p
	if prefix != "" {
		var ok bool
		if rest, ok = strings.CutPrefix(p, prefix+"/"); !ok {
			return "", false
		}
	}
	dir, _, ok := strings.Cut(rest, "/")
	return dir, ok
}

func assembleParts(files, anchors []*File, subject map[*File]*File, prefix string, big []string) ([][]*File, []*File) {
	slices.Sort(big)
	partOf := map[*File]int{}
	for _, f := range anchors {
		partOf[f] = len(big)
		if key, ok := nextDir(f.Path, prefix); ok {
			if i := slices.Index(big, key); i >= 0 {
				partOf[f] = i
			}
		}
	}
	parts := make([][]*File, len(big)+1)
	for _, f := range files {
		i, ok := partOf[f]
		if s, isTest := subject[f]; isTest {
			i, ok = partOf[s], true
		}
		if !ok {
			i = len(big)
		}
		parts[i] = append(parts[i], f)
	}
	return parts[:len(big)], parts[len(big)]
}

// withParts splits a group's files into child groups when it is large, and
// orders the children so parts others use come first.
func (g *grouper) withParts(grp Group, files []*File, docs []*File) Group {
	parts, dirs, rest := splitParts(files)
	if parts == nil {
		return grp
	}
	children := make([]Group, 0, len(parts)+1)
	partOf := map[string]int{}
	add := func(part []*File, title string, rank int) {
		slices.SortFunc(part, func(a, b *File) int { return strings.Compare(a.Path, b.Path) })
		child := g.newGroup(grp.Kind, title, part)
		child.Summary = append(changeSummary(part), g.docsSummary(part, docs)...)
		child.rank = rank
		for _, f := range part {
			partOf[f.Path] = len(children)
		}
		children = append(children, child)
	}
	for _, part := range parts {
		lines := 0
		for _, f := range part {
			lines += f.Additions + f.Deletions
		}
		add(part, contentTitle(part), -lines)
	}
	if len(rest) > 0 {
		add(rest, "Other files", 1)
	}
	children = g.order(children, partOf)
	for i := range children {
		children[i].ID = i + 1
	}
	where := make([]string, 0, len(dirs)+1)
	for _, d := range dirs {
		where = append(where, dirLabel(d))
	}
	if len(rest) > 0 {
		where = append(where, "other files")
	}
	grp.Children = children
	grp.Summary = []string{fmt.Sprintf("In %d parts by directory: %s.", len(children), joinAnd(where))}
	return grp
}
