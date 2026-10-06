// Package review breaks a change down for a reviewer: what each changed file
// is for, which files are safe to skim, and which declarations changed. It
// works from diffs and file contents supplied by the caller and never runs
// git; see the gitdiff package for reading a range from a repository.
package review

import (
	"bytes"
	"slices"
	"strings"

	"github.com/git-pkgs/manifests"
	"github.com/git-pkgs/roles"
)

// MaxContentBytes is the most file content worth supplying per side. Larger
// files are still classified from their first bytes but not outlined.
const MaxContentBytes = 1 << 20

const maxContexts = 3

// Skip reasons that don't come from roles.
const (
	skipBinary    = "binary"
	skipSubmodule = "submodule"
)

// File statuses.
const (
	Added       = "added"
	Modified    = "modified"
	Deleted     = "deleted"
	Renamed     = "renamed"
	Copied      = "copied"
	TypeChanged = "type-changed"
)

// skipRoles mark files a reviewer can skim: their content is produced by a tool
// or copied from elsewhere.
var skipRoles = []roles.Role{roles.Generated, roles.Vendor, roles.BuildOutput, roles.Minified, roles.Cache}

// Content is one side of a changed file. Complete is false when Data holds
// only a prefix.
type Content struct {
	Data     []byte
	Complete bool
}

// FileDiff is one changed file as a diff describes it. Base, Head, Symbols
// and BaseSymbols are optional: without contents, classification is by path
// alone and declarations are not compared; without Symbols, changes are
// described by hunk context rather than by name; without BaseSymbols, a
// declaration whose every line changed is taken to be new.
type FileDiff struct {
	Path       string
	OldPath    string
	Status     string
	Similarity int
	Additions  int
	Deletions  int
	Binary     bool
	Symlink    bool
	Submodule  bool
	// OldMode and NewMode are git file modes such as 100644, empty for a
	// side that doesn't exist or when the diff doesn't say.
	OldMode string
	NewMode string
	Hunks   []Hunk
	Base    *Content
	Head    *Content
	Symbols []Symbol
	// BaseSymbols are the declarations in the base version.
	BaseSymbols []Symbol
}

// Symbol is a declaration in one version of a file, with one-based inclusive
// line numbers. Qualified names it within the file, such as "Store.load",
// and Signature is its text up to any body, such as
// "func (b *B) Load(x int) error".
type Symbol struct {
	Name      string `json:"name"`
	Qualified string `json:"qualified,omitempty"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Exported  bool   `json:"exported,omitempty"`
}

// SymbolChange says how a head declaration was affected by the change.
type SymbolChange struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Exported bool   `json:"exported,omitempty"`
	Change   string `json:"change"`
}

// Symbol change kinds.
const (
	SymbolAdded     = "added"
	SymbolSignature = "signature"
	SymbolBody      = "body"
)

// Dependency marks a file a package manager reads, as identified by manifests.
type Dependency struct {
	Ecosystem string `json:"ecosystem"`
	Kind      string `json:"kind"`
}

// Commit is one commit in the change, oldest first.
type Commit struct {
	SHA     string       `json:"sha,omitempty"`
	Subject string       `json:"subject"`
	Body    string       `json:"body,omitempty"`
	Files   []CommitFile `json:"files,omitempty"`
}

// CommitFile is one path a commit changed and by how much. OldPath is set
// when the commit renamed it.
type CommitFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// Link records that changed code in From uses changed code in To.
type Link struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Range is the pair of commits a review covers.
type Range struct {
	Base      string `json:"base"`
	Head      string `json:"head"`
	BaseRev   string `json:"base_rev"`
	HeadRev   string `json:"head_rev"`
	MergeBase bool   `json:"merge_base"`
}

// File is the analysis of one changed path.
type File struct {
	Path       string           `json:"path"`
	OldPath    string           `json:"old_path,omitempty"`
	Status     string           `json:"status"`
	Similarity int              `json:"similarity,omitempty"`
	Additions  int              `json:"additions"`
	Deletions  int              `json:"deletions"`
	Binary     bool             `json:"binary,omitempty"`
	OldMode    string           `json:"old_mode,omitempty"`
	NewMode    string           `json:"new_mode,omitempty"`
	Roles      []roles.Role     `json:"roles"`
	Evidence   []roles.Evidence `json:"evidence"`
	Hunks      int              `json:"hunks"`
	Contexts   []string         `json:"contexts,omitempty"`
	Dependency *Dependency      `json:"dependency,omitempty"`
	Skip       string           `json:"skip,omitempty"`
	Mechanical string           `json:"mechanical,omitempty"`
	Surface    *Surface         `json:"surface,omitempty"`
	Symbols    []SymbolChange   `json:"symbols,omitempty"`

	fingerprint        string
	literalFingerprint string
}

// Overview is the breakdown of one net diff. Callers fill Range, Branch and
// Links as available, Add each file, then call Group.
type Overview struct {
	Range  *Range `json:"range,omitempty"`
	Branch string `json:"branch,omitempty"`
	// Commits is optional metadata; grouping and rendering use the net diff.
	Commits   []Commit `json:"commits,omitempty"`
	Files     []File   `json:"files"`
	Links     []Link   `json:"links,omitempty"`
	Groups    []Group  `json:"groups"`
	Additions int      `json:"additions"`
	Deletions int      `json:"deletions"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Add appends an analysed file and updates the totals.
func (o *Overview) Add(f File) {
	o.Files = append(o.Files, f)
	o.Additions += f.Additions
	o.Deletions += f.Deletions
}

// Analyze classifies one changed file and compares its declarations when both
// sides' contents are available.
func Analyze(d FileDiff) (File, error) {
	f := File{
		Path:       d.Path,
		OldPath:    d.OldPath,
		Status:     d.Status,
		Similarity: d.Similarity,
		Additions:  d.Additions,
		Deletions:  d.Deletions,
		Binary:     d.Binary,
		OldMode:    d.OldMode,
		NewMode:    d.NewMode,
		Hunks:      len(d.Hunks),
		Contexts:   hunkContexts(d.Hunks),
	}
	if eco, kind, ok := manifests.Identify(d.Path); ok {
		f.Dependency = &Dependency{Ecosystem: eco, Kind: string(kind)}
	}
	if d.Submodule {
		result, err := roles.Classify(d.Path)
		if err != nil {
			return f, err
		}
		f.Roles, f.Evidence, f.Skip = result.Roles, result.Evidence, skipSubmodule
		return f, nil
	}

	current := d.Head
	if d.Status == Deleted {
		current = d.Base
	}
	result, err := classify(d.Path, current)
	if err != nil {
		return f, err
	}
	f.Roles, f.Evidence = result.Roles, result.Evidence
	f.Skip = skipReason(result, f.Dependency, d.Binary)
	if f.Skip != "" || d.Binary || d.Symlink {
		return f, nil
	}
	f.Mechanical = mechanicalReason(d)
	f.fingerprint = editFingerprint(d.Hunks)
	f.literalFingerprint = literalFingerprint(d.Hunks)
	f.Symbols = symbolChanges(d)

	base, okBase := side(d.Base, d.Status == Added)
	head, okHead := side(d.Head, d.Status == Deleted)
	if okBase && okHead && (d.Status == Added || d.Status == Deleted || !bytes.Equal(base, head)) {
		f.Surface = diffSurface(d.Path, base, head)
	}
	return f, nil
}

// side returns complete contents, or nil and true for a side the file doesn't
// have.
func side(c *Content, absent bool) ([]byte, bool) {
	if absent {
		return nil, true
	}
	if c == nil || !c.Complete {
		return nil, false
	}
	if c.Data == nil {
		return []byte{}, true
	}
	return c.Data, true
}

func classify(name string, c *Content) (roles.Result, error) {
	switch {
	case c == nil:
		return roles.Classify(name)
	case c.Complete:
		r, err := roles.ClassifyBlob(name, c.Data)
		return r.Result, err
	default:
		r, err := roles.ClassifyPrefix(name, c.Data)
		return r.Result, err
	}
}

func skipReason(result roles.Result, dep *Dependency, binary bool) string {
	for _, role := range skipRoles {
		if !result.Has(role) {
			continue
		}
		for _, e := range result.Evidence {
			if e.Role == role && e.Subtype != "" {
				return string(role) + " (" + e.Subtype + ")"
			}
		}
		return string(role)
	}
	if dep != nil && dep.Kind != string(manifests.Manifest) {
		if dep.Kind == string(manifests.Supplement) {
			return "generated (lockfile supplement)"
		}
		return "generated (" + dep.Kind + ")"
	}
	if binary {
		return skipBinary
	}
	return ""
}

func mechanicalReason(d FileDiff) string {
	switch {
	case (d.Status == Renamed || d.Status == Copied) && d.Additions == 0 && d.Deletions == 0:
		return d.Status + " without changes"
	case d.Status == Modified && whitespaceOnly(d):
		return "whitespace only"
	}
	return ""
}

// hunkContexts returns the distinct enclosing-scope hints diff tools put after
// a hunk's line ranges.
func hunkContexts(hunks []Hunk) []string {
	var contexts []string
	for _, h := range hunks {
		_, rest, ok := strings.Cut(strings.TrimPrefix(h.Header, "@@"), "@@")
		if !ok {
			continue
		}
		ctx := strings.TrimSpace(rest)
		if ctx == "" || slices.Contains(contexts, ctx) {
			continue
		}
		contexts = append(contexts, ctx)
		if len(contexts) == maxContexts {
			break
		}
	}
	return contexts
}
