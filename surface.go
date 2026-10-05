package review

import (
	"strings"
	"unicode"

	"github.com/git-pkgs/outline"
)

// maxSurfaceCells caps the LCS table so a huge skeleton can't stall a run.
const maxSurfaceCells = 4_000_000

// SurfaceLine is one declaration-level line that differs between the base and
// head outlines. Context is the enclosing top-level declaration for indented
// lines such as struct fields or methods.
type SurfaceLine struct {
	Op      string `json:"op"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

// Surface compares the outlines (signatures, types, imports, comments) of a
// file's two versions. An empty Lines with Truncated false means only
// function bodies changed.
type Surface struct {
	Lines     []SurfaceLine `json:"lines"`
	Truncated bool          `json:"truncated,omitempty"`
}

// BodyOnly reports whether the change left every declaration untouched.
func (s *Surface) BodyOnly() bool { return len(s.Lines) == 0 && !s.Truncated }

type skeletonLine struct {
	text    string
	context string
}

// skeleton returns the declaration lines of src, or false when outline does
// not support the file.
func skeleton(src []byte, name string) ([]skeletonLine, bool) {
	if src == nil {
		return nil, outline.Supported(name)
	}
	out, ok := outline.Outline(src, name)
	if !ok {
		return nil, false
	}
	var lines []skeletonLine
	var top string
	for raw := range strings.SplitSeq(out, "\n") {
		text := strings.TrimRightFunc(raw, unicode.IsSpace)
		if text == "" || strings.TrimSpace(text) == "⋮----" || !hasWord(text) {
			continue
		}
		indented := text[0] == ' ' || text[0] == '\t'
		if !indented {
			top = text
		}
		line := skeletonLine{text: strings.TrimSpace(text)}
		if indented {
			line.context = top
		}
		lines = append(lines, line)
	}
	return lines, true
}

func hasWord(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0
}

// diffSurface returns nil when the file's language has no outline support.
func diffSurface(name string, base, head []byte) *Surface {
	a, okA := skeleton(base, name)
	b, okB := skeleton(head, name)
	if !okA || !okB {
		return nil
	}
	if len(a)*len(b) > maxSurfaceCells {
		return &Surface{Lines: []SurfaceLine{}, Truncated: true}
	}
	return &Surface{Lines: lcsDiff(a, b)}
}

func lcsDiff(a, b []skeletonLine) []SurfaceLine {
	n, m := len(a), len(b)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i].text == b[j].text {
				table[i][j] = table[i+1][j+1] + 1
			} else {
				table[i][j] = max(table[i+1][j], table[i][j+1])
			}
		}
	}
	lines := []SurfaceLine{}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i].text == b[j].text:
			i++
			j++
		case i < n && (j == m || table[i+1][j] >= table[i][j+1]):
			lines = append(lines, SurfaceLine{Op: "-", Text: a[i].text, Context: a[i].context})
			i++
		default:
			lines = append(lines, SurfaceLine{Op: "+", Text: b[j].text, Context: b[j].context})
			j++
		}
	}
	return lines
}
