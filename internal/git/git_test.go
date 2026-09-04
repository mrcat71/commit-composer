package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// testRepo builds a throwaway git repo with `n` commits and returns a Repo
// rooted there. Commit subjects are "c1", "c2", ..., bodies are empty.
func testRepo(t *testing.T, n int) Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	r := Repo{Dir: dir}
	ctx := context.Background()
	mustRun(t, r, ctx, "init", "-q", "-b", "main")
	mustRun(t, r, ctx, "config", "user.email", "test@example.invalid")
	mustRun(t, r, ctx, "config", "user.name", "Test User")
	mustRun(t, r, ctx, "config", "commit.gpgsign", "false")
	for i := 1; i <= n; i++ {
		name := "f" + itoa(i) + ".txt"
		path := filepath.Join(dir, name)
		body := "content " + itoa(i) + "\n"
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		mustRun(t, r, ctx, "add", name)
		mustRun(t, r, ctx, "commit", "-q", "-m", "c"+itoa(i))
	}
	return r
}

func mustRun(t *testing.T, r Repo, ctx context.Context, args ...string) {
	t.Helper()
	if _, err := r.Run(ctx, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestRevParseAndIsClean(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 2)
	ctx := context.Background()

	sha, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse HEAD: %v", err)
	}
	if len(sha) != 40 {
		t.Fatalf("expected full sha, got %q", sha)
	}

	clean, err := r.IsClean(ctx)
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Fatal("expected fresh repo to be clean")
	}

	// Dirty the tree.
	if err := os.WriteFile(filepath.Join(r.Dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty: %v", err)
	}
	clean, err = r.IsClean(ctx)
	if err != nil {
		t.Fatalf("IsClean dirty: %v", err)
	}
	if clean {
		t.Fatal("expected dirty tree to be reported")
	}
}

func TestResolveRange(t *testing.T) {
	t.Parallel()
	// 30 commits so the default-depth (25) fallback resolves.
	r := testRepo(t, 30)
	ctx := context.Background()

	tests := []struct {
		name    string
		spec    string
		wantOps int // expected number of commits in resulting Log()
		wantErr bool
	}{
		{"single rev HEAD~3", "HEAD~3", 3, false},
		{"explicit base..head", "HEAD~4..HEAD", 4, false},
		{"empty falls back to all-but-root", "", 29, false},
		{"bad rev", "not-a-real-rev", 0, true},
		{"triple dot rejected", "HEAD~3...HEAD", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, head, rs, err := r.ResolveRange(ctx, tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got base=%s head=%s rs=%s", base, head, rs)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveRange: %v", err)
			}
			commits, err := r.Log(ctx, base, head)
			if err != nil {
				t.Fatalf("Log: %v", err)
			}
			if len(commits) != tc.wantOps {
				t.Fatalf("got %d commits, want %d (rs=%s)", len(commits), tc.wantOps, rs)
			}
		})
	}
}

// TestResolveRangeSingleCommit regresses the case where a fresh repo has
// only the initial commit: the TUI would error out instead of opening so
// the user could not start recomposing at all. The fix returns base=""
// (empty-tree sentinel) and head=HEAD so Log/Diff/Apply can fall back to
// the empty tree as parent.
func TestResolveRangeSingleCommit(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 1)
	ctx := context.Background()

	base, head, rs, err := r.ResolveRange(ctx, "")
	if err != nil {
		t.Fatalf("ResolveRange single commit: %v", err)
	}
	if base != "" {
		t.Errorf("base=%q want empty sentinel for single-commit repo", base)
	}
	if len(head) != 40 {
		t.Errorf("head=%q want full SHA", head)
	}
	if rs != "HEAD" {
		t.Errorf("range spec=%q want HEAD", rs)
	}

	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log with empty base: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("got %d commits, want 1", len(commits))
	}
	if commits[0].Subject != "c1" {
		t.Errorf("subject=%q want c1", commits[0].Subject)
	}
}

// TestResolveRangeNoCommits ensures a brand-new repo (no commits at all)
// returns a friendly error rather than a cryptic git failure.
func TestResolveRangeNoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	dir := t.TempDir()
	r := Repo{Dir: dir}
	ctx := context.Background()
	mustRun(t, r, ctx, "init", "-q", "-b", "main")

	_, _, _, err := r.ResolveRange(ctx, "")
	if err == nil {
		t.Fatal("expected error on empty repo")
	}
	if !strings.Contains(err.Error(), "no commits yet") {
		t.Errorf("error %q does not mention no commits", err.Error())
	}
}

// TestResolveRangeDefaultWithMergeCommits regresses a bug where the default
// "all commits" fallback used rev-list --count (counting all reachable commits
// including merged-in branches) to pick HEAD~N. With merge commits in the
// history, count exceeds first-parent depth and HEAD~(count-1) fails to
// resolve. The fix is to use --first-parent --count instead.
func TestResolveRangeDefaultWithMergeCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	r := testRepo(t, 3) // c1, c2, c3 on master
	ctx := context.Background()
	// Branch off c2, add two commits, merge back.
	if _, err := r.Run(ctx, "checkout", "-b", "feature", "HEAD~1"); err != nil {
		t.Fatalf("checkout feature: %v", err)
	}
	mk := func(name string) {
		if err := os.WriteFile(filepath.Join(r.Dir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := r.Run(ctx, "add", name); err != nil {
			t.Fatalf("add: %v", err)
		}
		if _, err := r.Run(ctx, "commit", "-q", "-m", name); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	mk("feat-a.txt")
	mk("feat-b.txt")
	if _, err := r.Run(ctx, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
	if _, err := r.Run(ctx, "merge", "--no-ff", "--no-edit", "feature"); err != nil {
		t.Fatalf("merge feature: %v", err)
	}
	// rev-list --count HEAD reports 6 (3 master + 2 feature + 1 merge),
	// but first-parent depth is 4 (3 master + 1 merge).
	if _, _, _, err := r.ResolveRange(ctx, ""); err != nil {
		t.Fatalf("ResolveRange with merge commits should not error, got: %v", err)
	}
}

// TestResolveRangeSyncedBranch covers the case where the current branch
// tracks an upstream that already points at HEAD (no commits ahead). The
// default range should fall through to HEAD~N..HEAD instead of erroring on
// an empty upstream..HEAD range.
func TestResolveRangeSyncedBranch(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 5)
	ctx := context.Background()
	// Fake an upstream by creating a remote-tracking ref at HEAD and wiring
	// branch.<name>.{remote,merge} so @{upstream} resolves. No real remote.
	if _, err := r.Run(ctx, "update-ref", "refs/remotes/origin/main", "HEAD"); err != nil {
		t.Fatalf("update-ref: %v", err)
	}
	if _, err := r.Run(ctx, "config", "branch.main.remote", "origin"); err != nil {
		t.Fatalf("config branch.main.remote: %v", err)
	}
	if _, err := r.Run(ctx, "config", "branch.main.merge", "refs/heads/main"); err != nil {
		t.Fatalf("config branch.main.merge: %v", err)
	}
	base, head, rs, err := r.ResolveRange(ctx, "")
	if err != nil {
		t.Fatalf("ResolveRange empty on synced branch: %v", err)
	}
	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	// 5 commits in repo, fallback uses HEAD~min(10, 4)..HEAD = HEAD~4..HEAD = 4 commits.
	if len(commits) != 4 {
		t.Fatalf("expected 4 commits from fallback, got %d (rs=%s)", len(commits), rs)
	}
}

// TestResolveRangeJustAheadOfUpstream covers the post-PR-merge case: the
// branch tracks an upstream and is ahead by only one commit. We want the
// default range to expand to the recent-N window for context rather than
// hand the user a single-row TUI. See minUpstreamCommitsForDefault.
func TestResolveRangeJustAheadOfUpstream(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 5)
	ctx := context.Background()
	// Point the fake upstream one commit behind HEAD so cnt(upstream..HEAD) == 1.
	if _, err := r.Run(ctx, "update-ref", "refs/remotes/origin/main", "HEAD~1"); err != nil {
		t.Fatalf("update-ref: %v", err)
	}
	if _, err := r.Run(ctx, "config", "branch.main.remote", "origin"); err != nil {
		t.Fatalf("config branch.main.remote: %v", err)
	}
	if _, err := r.Run(ctx, "config", "branch.main.merge", "refs/heads/main"); err != nil {
		t.Fatalf("config branch.main.merge: %v", err)
	}
	base, head, rs, err := r.ResolveRange(ctx, "")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	// We should have fallen back to HEAD~4..HEAD (4 commits), NOT
	// upstream..HEAD (which would have given 1).
	if len(commits) != 4 {
		t.Fatalf("expected fallback to HEAD~4..HEAD (4 commits), got %d (rs=%s)", len(commits), rs)
	}
	if strings.Contains(rs, "origin/") {
		t.Fatalf("expected fallback range, got upstream-based range %q", rs)
	}
}

func TestLogOrderAndFields(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 4) // need a parent for HEAD~3
	ctx := context.Background()
	base, head, _, err := r.ResolveRange(ctx, "HEAD~3")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("len=%d want 3", len(commits))
	}
	// Repo has c1..c4; HEAD~3..HEAD excludes c1, so we expect c2, c3, c4 in order.
	if commits[0].Subject != "c2" || commits[2].Subject != "c4" {
		t.Fatalf("expected oldest-first order [c2, c3, c4], got [%s, %s, %s]",
			commits[0].Subject, commits[1].Subject, commits[2].Subject)
	}
	for i, c := range commits {
		if len(c.SHA) != 40 {
			t.Errorf("commit %d: expected full SHA, got %q", i, c.SHA)
		}
		if c.Short == "" {
			t.Errorf("commit %d: empty Short", i)
		}
		if c.Author == "" || c.Email == "" {
			t.Errorf("commit %d: empty author info", i)
		}
		if c.Date.IsZero() {
			t.Errorf("commit %d: zero date", i)
		}
	}
}

func TestFilesAndDiff(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 1)
	ctx := context.Background()
	sha, err := r.RevParse(ctx, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	files, err := r.Files(ctx, sha)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if len(files) != 1 || files[0].Path != "f1.txt" || files[0].Status != "A" {
		t.Fatalf("Files: got %+v", files)
	}
	diff, err := r.Diff(ctx, sha)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(diff, "diff --git") || !strings.Contains(diff, "+content 1") {
		t.Fatalf("Diff missing expected content:\n%s", diff)
	}
}

func TestCommitsContainedIn(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 3)
	ctx := context.Background()

	// A branch pointing at HEAD~1 acts as the "protected" ref: it contains
	// HEAD~1 and HEAD~2 but not HEAD.
	if _, err := r.Run(ctx, "branch", "protected", "HEAD~1"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	sha := func(rev string) string {
		t.Helper()
		s, err := r.RevParse(ctx, rev)
		if err != nil {
			t.Fatalf("RevParse %s: %v", rev, err)
		}
		return s
	}
	head, head1, head2 := sha("HEAD"), sha("HEAD~1"), sha("HEAD~2")

	tests := []struct {
		name string
		ref  string
		revs []string
		want map[string]bool
	}{
		{"all contained", "protected", []string{head1, head2}, map[string]bool{head1: true, head2: true}},
		{"none contained", "protected", []string{head}, map[string]bool{head: false}},
		{"mixed", "protected", []string{head, head1, head2}, map[string]bool{head: false, head1: true, head2: true}},
		{"ref is head itself", "main", []string{head, head1, head2}, map[string]bool{head: true, head1: true, head2: true}},
		{"empty input", "protected", nil, map[string]bool{}},
		// A missing ref means nothing is published, so nothing is contained.
		// It must not surface as an error - the caller treats an error as
		// "skip this ref" and we want the same answer either way.
		{"unknown ref", "origin/does-not-exist", []string{head, head1}, map[string]bool{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := r.CommitsContainedIn(ctx, tc.ref, tc.revs)
			if err != nil {
				t.Fatalf("CommitsContainedIn: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for s, want := range tc.want {
				if got[s] != want {
					t.Errorf("%s: contained=%v want %v", s[:8], got[s], want)
				}
			}
		})
	}
}

func TestSubjects(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 3)
	ctx := context.Background()
	sha := func(rev string) string {
		t.Helper()
		s, err := r.RevParse(ctx, rev)
		if err != nil {
			t.Fatalf("RevParse %s: %v", rev, err)
		}
		return s
	}
	c3, c2, c1 := sha("HEAD"), sha("HEAD~1"), sha("HEAD~2")
	missing := strings.Repeat("0", 40)

	tests := []struct {
		name string
		in   []string
		want map[string]string
	}{
		{"single", []string{c2}, map[string]string{c2: "c2"}},
		{"all three", []string{c1, c2, c3}, map[string]string{c1: "c1", c2: "c2", c3: "c3"}},
		{"reversed order", []string{c3, c1}, map[string]string{c3: "c3", c1: "c1"}},
		{"duplicates collapse", []string{c1, c1}, map[string]string{c1: "c1"}},
		{"empty input", nil, map[string]string{}},
		// A bad SHA fails the batched call; the per-SHA fallback must still
		// resolve the good ones.
		{"unresolvable sha among good ones", []string{c1, missing, c3}, map[string]string{c1: "c1", c3: "c3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := r.Subjects(ctx, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d entries %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for s, want := range tc.want {
				if got[s] != want {
					t.Errorf("%s: subject=%q want %q", s[:8], got[s], want)
				}
			}
		})
	}
}

// porcelainStatus captures the full working-tree state as git sees it.
// UncommittedDiff must leave this byte-identical: it stages untracked files to
// make them visible to `git diff`, and doing that in the user's real index
// would show up here as staged additions.
func porcelainStatus(t *testing.T, r Repo) string {
	t.Helper()
	out, err := r.Run(context.Background(), "status", "--porcelain")
	if err != nil {
		t.Fatalf("git status --porcelain: %v", err)
	}
	return out
}

func TestUncommittedDiff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// write creates an untracked file; stage additionally adds it to the index.
	write := func(t *testing.T, r Repo, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	tests := []struct {
		name string
		// setup dirties the tree and returns substrings the diff must contain.
		setup     func(t *testing.T, r Repo) []string
		wantEmpty bool
	}{
		{
			name:      "clean tree",
			setup:     func(t *testing.T, r Repo) []string { return nil },
			wantEmpty: true,
		},
		{
			name: "modified tracked file",
			setup: func(t *testing.T, r Repo) []string {
				write(t, r, "f1.txt", "changed\n")
				return []string{"f1.txt", "+changed"}
			},
		},
		{
			name: "staged change",
			setup: func(t *testing.T, r Repo) []string {
				write(t, r, "f1.txt", "staged edit\n")
				mustRun(t, r, ctx, "add", "f1.txt")
				return []string{"f1.txt", "+staged edit"}
			},
		},
		{
			name: "untracked file appears as an addition",
			setup: func(t *testing.T, r Repo) []string {
				write(t, r, "new.txt", "brand new\n")
				return []string{"new.txt", "+brand new"}
			},
		},
		{
			name: "untracked plus tracked plus staged together",
			setup: func(t *testing.T, r Repo) []string {
				write(t, r, "f1.txt", "unstaged edit\n")
				write(t, r, "f2.txt", "will be staged\n")
				mustRun(t, r, ctx, "add", "f2.txt")
				write(t, r, "brand-new.txt", "untracked body\n")
				return []string{"+unstaged edit", "+will be staged", "+untracked body"}
			},
		},
		{
			name: "untracked file inside a new directory",
			setup: func(t *testing.T, r Repo) []string {
				if err := os.MkdirAll(filepath.Join(r.Dir, "sub", "deep"), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				write(t, r, filepath.Join("sub", "deep", "nested.txt"), "nested body\n")
				return []string{"sub/deep/nested.txt", "+nested body"}
			},
		},
		{
			name: "file with a space in its name",
			setup: func(t *testing.T, r Repo) []string {
				write(t, r, "two words.txt", "spaced body\n")
				return []string{"two words.txt", "+spaced body"}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := testRepo(t, 2)
			want := tc.setup(t, r)
			before := porcelainStatus(t, r)

			diff, err := r.UncommittedDiff(ctx)
			if err != nil {
				t.Fatalf("UncommittedDiff: %v", err)
			}
			if tc.wantEmpty {
				if strings.TrimSpace(diff) != "" {
					t.Errorf("expected empty diff for clean tree, got:\n%s", diff)
				}
			}
			for _, sub := range want {
				if !strings.Contains(diff, sub) {
					t.Errorf("diff missing %q:\n%s", sub, diff)
				}
			}
			if after := porcelainStatus(t, r); after != before {
				t.Errorf("UncommittedDiff mutated the index\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

// TestUncommittedDiffManyUntracked exercises the pathspec chunking: with more
// untracked files than maxPathsPerCall, every one must still land in the diff.
func TestUncommittedDiffManyUntracked(t *testing.T) {
	t.Parallel()
	r := testRepo(t, 1)
	ctx := context.Background()

	const n = maxPathsPerCall + 20
	for i := range n {
		name := "u" + itoa(i) + ".txt"
		if err := os.WriteFile(filepath.Join(r.Dir, name), []byte("body "+itoa(i)+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	before := porcelainStatus(t, r)

	diff, err := r.UncommittedDiff(ctx)
	if err != nil {
		t.Fatalf("UncommittedDiff: %v", err)
	}
	for i := range n {
		if !strings.Contains(diff, "u"+itoa(i)+".txt") {
			t.Fatalf("diff missing u%d.txt (chunk boundary lost files)", i)
		}
	}
	if after := porcelainStatus(t, r); after != before {
		t.Errorf("UncommittedDiff mutated the index\nbefore len=%d after len=%d", len(before), len(after))
	}
}

// TestUncommittedDiffKeepsGoodPathsOnBadPath covers the per-path retry: an
// unreadable file among several must not cost us the readable ones.
func TestUncommittedDiffKeepsGoodPathsOnBadPath(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not deny access")
	}
	r := testRepo(t, 1)
	ctx := context.Background()

	for _, name := range []string{"good-a.txt", "bad.txt", "good-b.txt"} {
		if err := os.WriteFile(filepath.Join(r.Dir, name), []byte("body of "+name+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	bad := filepath.Join(r.Dir, "bad.txt")
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("chmod bad.txt: %v", err)
	}
	// Restore the mode so t.TempDir cleanup can remove it.
	t.Cleanup(func() { _ = os.Chmod(bad, 0o644) })

	diff, err := r.UncommittedDiff(ctx)
	if err != nil {
		t.Fatalf("UncommittedDiff: %v", err)
	}
	for _, name := range []string{"good-a.txt", "good-b.txt"} {
		if !strings.Contains(diff, name) {
			t.Errorf("diff lost readable file %s:\n%s", name, diff)
		}
	}
}
