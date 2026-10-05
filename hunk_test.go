package review

import "testing"

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		header string
		want   Hunk
	}{
		{"@@ -1,3 +1,4 @@ func A() {", Hunk{OldStart: 1, OldLines: 3, NewStart: 1, NewLines: 4}},
		{"@@ -5 +5 @@", Hunk{OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1}},
		{"@@ -0,0 +1,2 @@", Hunk{OldStart: 0, OldLines: 0, NewStart: 1, NewLines: 2}},
		{"@@ -7,2 +6,0 @@", Hunk{OldStart: 7, OldLines: 2, NewStart: 6, NewLines: 0}},
	}
	for _, tt := range tests {
		got, err := ParseHunkHeader(tt.header)
		if err != nil {
			t.Fatal(err)
		}
		tt.want.Header = tt.header
		if got.OldStart != tt.want.OldStart || got.OldLines != tt.want.OldLines ||
			got.NewStart != tt.want.NewStart || got.NewLines != tt.want.NewLines || got.Header != tt.header {
			t.Errorf("ParseHunkHeader(%q) = %+v", tt.header, got)
		}
	}
	for _, bad := range []string{"@@ -x +1 @@", "@@ -1", "-1 +1", "@@ 1 2 @@"} {
		if _, err := ParseHunkHeader(bad); err == nil {
			t.Errorf("ParseHunkHeader(%q) accepted", bad)
		}
	}
}

func TestChangedLines(t *testing.T) {
	c := changedLines([]Hunk{
		{NewStart: 3, NewLines: 3, Lines: []string{" a", "-b", "+B", " c"}},
		{NewStart: 10, NewLines: 0, Lines: []string{"-gone"}},
	})
	if !c.added[4] || c.added[3] || c.added[5] {
		t.Errorf("added = %v", c.added)
	}
	if !c.touched[4] || !c.touched[10] || !c.touched[11] {
		t.Errorf("touched = %v", c.touched)
	}
	if !c.all(4, 4) || c.all(3, 4) || c.all(5, 4) {
		t.Error("all reports the wrong ranges")
	}
	if !c.any(1, 4) || c.any(5, 9) {
		t.Error("any reports the wrong ranges")
	}
}

func TestWhitespaceOnly(t *testing.T) {
	full := func(path, base, head string) FileDiff {
		return FileDiff{Path: path, Status: Modified,
			Base: &Content{Data: []byte(base), Complete: true}, Head: &Content{Data: []byte(head), Complete: true}}
	}
	hunks := func(path string, hs ...[]string) FileDiff {
		d := FileDiff{Path: path, Status: Modified}
		for _, lines := range hs {
			d.Hunks = append(d.Hunks, Hunk{OldLines: 1, NewLines: 1, Lines: lines})
		}
		return d
	}
	tests := []struct {
		name string
		d    FileDiff
		want bool
	}{
		{"reindented", full("a.go", "func A() {\n\treturn 1\n}\n", "func A() {\n    return 1\n}\n"), true},
		{"reordered", full("flow.go", "func Run() {\n authorize()\n execute()\n}\n", "func Run() {\n execute()\n authorize()\n}\n"), false},
		{"words joined", full("a.go", "return x\n", "returnx\n"), false},
		{"identical", full("a.go", "x\n", "x\n"), false},
		{"python block changed", full("m.py", "if x:\n    a()\nb()\n", "if x:\n    a()\n    b()\n"), false},
		{"python trailing space", full("m.py", "a()  \n\nb()\n", "a()\nb()\n"), true},
		{"yaml nesting changed", full("c.yml", "a:\n  b: 1\nc: 2\n", "a:\n  b: 1\n  c: 2\n"), false},
		{"makefile", full("Makefile", "all:\n\tgo build\n", "all:\n    go build\n"), false},
		{"hunk spacing", hunks("a.go", []string{" ctx", "-if x {", "+if x  {"}), true},
		{"hunk reorder", hunks("a.go", []string{"-a()", "-b()", "+b()", "+a()"}), false},
		{"moved between hunks", hunks("a.go", []string{" one", "-a()"}, []string{" two", "+a()"}), false},
		{"hunk without lines", FileDiff{Path: "a.go", Status: Modified, Hunks: []Hunk{{OldLines: 1, NewLines: 1}}}, false},
		{"nothing to compare", FileDiff{Path: "a.go", Status: Modified}, false},
	}
	for _, tt := range tests {
		if got := whitespaceOnly(tt.d); got != tt.want {
			t.Errorf("%s: whitespaceOnly = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLiteralFingerprint(t *testing.T) {
	fp := func(lines ...string) string { return literalFingerprint([]Hunk{{Lines: lines}}) }
	de := fp(`-    "title": "Alt"`, `+    "title": "Neu",`, `+    "cta": "Los geht's"`)
	fr := fp(`-    "title": "Ancien"`, `+    "title": "Nouveau",`, `+    "cta": "C'est parti"`)
	if de != fr {
		t.Errorf("translations of the same keys differ:\n%q\n%q", de, fr)
	}
	if fp(`+    "other": "Los"`) == fp(`+    "cta": "Los"`) {
		t.Error("different keys share a fingerprint")
	}
	if fp("-const TTL = 30", "+const TTL = 300") == fp("-const Retries = 3", "+const Retries = 0") {
		t.Error("different constants share a fingerprint")
	}
	if fp("-return false", "+return true") == fp("-return true", "+return false") {
		t.Error("boolean flips share a fingerprint")
	}
	for _, pair := range [][2]string{
		{"+return `${checkPermission(user)}`", "+return `${cache.get(key)}`"},
		{`+puts "#{user.name}"`, `+puts "#{order.id}"`},
		{`+print(f"{user}")`, `+print(f"{order}")`},
		{`+echo "$HOME"`, `+echo "$PATH"`},
		{`+let s = "\(user)"`, `+let s = "\(order)"`},
	} {
		if fp(pair[0]) == fp(pair[1]) {
			t.Errorf("interpolated strings masked: %q and %q share a fingerprint", pair[0], pair[1])
		}
	}
	if fp(`+price = "$5"`) != fp(`+price = "$6"`) {
		t.Error("strings without interpolation stopped matching")
	}
	if fp("+x := v2") == fp("+x := v3") {
		t.Error("digits inside identifiers were masked")
	}
}

func TestEditFingerprint(t *testing.T) {
	a := []Hunk{{Lines: []string{" ctx one", "-old()", "+new()"}}}
	b := []Hunk{{Lines: []string{" ctx two", "-old()", "+new()"}}}
	c := []Hunk{{Lines: []string{"-old()", "+other()"}}}
	if editFingerprint(a) != editFingerprint(b) {
		t.Error("same edit with different context has different fingerprints")
	}
	if editFingerprint(a) == editFingerprint(c) {
		t.Error("different edits share a fingerprint")
	}
	if editFingerprint([]Hunk{{Header: "@@ -1 +1 @@"}}) != "" {
		t.Error("hunk without lines has a fingerprint")
	}
}
