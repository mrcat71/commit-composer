package git

import (
	"strings"
	"testing"
)

func TestParseHunks(t *testing.T) {
	tests := []struct {
		name      string
		diff      string
		wantCount int
		checks    func(t *testing.T, hs []Hunk)
	}{
		{
			name:      "empty",
			diff:      "",
			wantCount: 0,
		},
		{
			name:      "whitespace only",
			diff:      "   \n\n",
			wantCount: 0,
		},
		{
			name: "single file single hunk",
			diff: `diff --git a/foo.txt b/foo.txt
index abc..def 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,4 @@
 line1
-old
+new1
+new2
 line3
`,
			wantCount: 1,
			checks: func(t *testing.T, hs []Hunk) {
				h := hs[0]
				if h.File != "foo.txt" {
					t.Errorf("file=%q want foo.txt", h.File)
				}
				if h.OldStart != 1 || h.OldCount != 3 {
					t.Errorf("old=%d,%d want 1,3", h.OldStart, h.OldCount)
				}
				if h.NewStart != 1 || h.NewCount != 4 {
					t.Errorf("new=%d,%d want 1,4", h.NewStart, h.NewCount)
				}
				if !strings.Contains(h.Body, "+new1") {
					t.Errorf("body missing +new1: %q", h.Body)
				}
			},
		},
		{
			name: "two hunks one file",
			diff: `diff --git a/a.go b/a.go
index 1..2 100644
--- a/a.go
+++ b/a.go
@@ -1,2 +1,3 @@
 keep
+inserted
@@ -10,1 +11,1 @@
-removed
+added
`,
			wantCount: 2,
			checks: func(t *testing.T, hs []Hunk) {
				if hs[0].Index != 0 || hs[1].Index != 1 {
					t.Errorf("indices=%d,%d want 0,1", hs[0].Index, hs[1].Index)
				}
				if hs[1].OldStart != 10 || hs[1].NewStart != 11 {
					t.Errorf("h2 starts=%d,%d want 10,11", hs[1].OldStart, hs[1].NewStart)
				}
			},
		},
		{
			name: "two files",
			diff: `diff --git a/a b/a
--- a/a
+++ b/a
@@ -1 +1 @@
-old
+new
diff --git a/b b/b
--- a/b
+++ b/b
@@ -5,1 +5,1 @@
-x
+y
`,
			wantCount: 2,
			checks: func(t *testing.T, hs []Hunk) {
				if hs[0].File != "a" || hs[1].File != "b" {
					t.Errorf("files=%q,%q want a,b", hs[0].File, hs[1].File)
				}
			},
		},
		{
			name: "default count omitted",
			diff: `diff --git a/x b/x
--- a/x
+++ b/x
@@ -1 +1 @@
-a
+b
`,
			wantCount: 1,
			checks: func(t *testing.T, hs []Hunk) {
				if hs[0].OldCount != 1 || hs[0].NewCount != 1 {
					t.Errorf("counts=%d,%d want 1,1 (omitted defaults to 1)", hs[0].OldCount, hs[0].NewCount)
				}
			},
		},
		{
			name: "new file",
			diff: `diff --git a/new.txt b/new.txt
new file mode 100644
index 0000000..1234567
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+hello
+world
`,
			wantCount: 1,
			checks: func(t *testing.T, hs []Hunk) {
				if !strings.Contains(hs[0].FileHead, "new file mode") {
					t.Errorf("filehead lost mode metadata: %q", hs[0].FileHead)
				}
			},
		},
		{
			name: "rename",
			diff: `diff --git a/old.txt b/new.txt
similarity index 80%
rename from old.txt
rename to new.txt
--- a/old.txt
+++ b/new.txt
@@ -1 +1 @@
-x
+y
`,
			wantCount: 1,
			checks: func(t *testing.T, hs []Hunk) {
				if hs[0].File != "new.txt" || hs[0].OldFile != "old.txt" {
					t.Errorf("rename paths=%q->%q want old.txt->new.txt", hs[0].OldFile, hs[0].File)
				}
				if !strings.Contains(hs[0].FileHead, "rename from old.txt") {
					t.Errorf("rename head lost: %q", hs[0].FileHead)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hs, err := ParseHunks(tc.diff)
			if err != nil {
				t.Fatalf("ParseHunks: %v", err)
			}
			if len(hs) != tc.wantCount {
				t.Fatalf("got %d hunks, want %d: %+v", len(hs), tc.wantCount, hs)
			}
			if tc.checks != nil {
				tc.checks(t, hs)
			}
		})
	}
}

func TestBuildPatch_RoundTrip(t *testing.T) {
	diff := `diff --git a/a b/a
index 1..2 100644
--- a/a
+++ b/a
@@ -1,2 +1,3 @@
 keep
+inserted
+second
@@ -10,1 +12,1 @@
-removed
+added
diff --git a/b b/b
index 3..4 100644
--- a/b
+++ b/b
@@ -1 +1 @@
-x
+y
`
	hs, err := ParseHunks(diff)
	if err != nil {
		t.Fatalf("ParseHunks: %v", err)
	}
	patch := BuildPatch(hs)
	if !strings.Contains(patch, "diff --git a/a b/a") {
		t.Errorf("patch missing a/a header")
	}
	if !strings.Contains(patch, "diff --git a/b b/b") {
		t.Errorf("patch missing b/b header")
	}
	if strings.Count(patch, "@@ -1,2 +1,3 @@") != 1 {
		t.Errorf("patch lost first hunk header for a:\n%s", patch)
	}
}

func TestBuildPatch_PartialSelection(t *testing.T) {
	diff := `diff --git a/a b/a
--- a/a
+++ b/a
@@ -1,1 +1,1 @@
-old1
+new1
@@ -10,1 +10,1 @@
-old2
+new2
`
	hs, err := ParseHunks(diff)
	if err != nil {
		t.Fatalf("ParseHunks: %v", err)
	}
	// Keep only the second hunk.
	patch := BuildPatch([]Hunk{hs[1]})
	if strings.Contains(patch, "+new1") {
		t.Errorf("partial patch leaked first hunk:\n%s", patch)
	}
	if !strings.Contains(patch, "+new2") {
		t.Errorf("partial patch missing second hunk:\n%s", patch)
	}
}
