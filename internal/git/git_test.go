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

func TestLogOrderAndFields(t *testing.T) {
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
	r := testRepo(t, 3)
	ctx := context.Background()

	// Create a branch pointing at HEAD~1 to act as "protected".
	if _, err := r.Run(ctx, "branch", "protected", "HEAD~1"); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	headSHA, _ := r.RevParse(ctx, "HEAD")
	headMinus1, _ := r.RevParse(ctx, "HEAD~1")
	headMinus2, _ := r.RevParse(ctx, "HEAD~2")

	got, err := r.CommitsContainedIn(ctx, "protected", []string{headSHA, headMinus1, headMinus2})
	if err != nil {
		t.Fatalf("CommitsContainedIn: %v", err)
	}
	if got[headSHA] {
		t.Errorf("HEAD should NOT be in protected, got contained=true")
	}
	if !got[headMinus1] || !got[headMinus2] {
		t.Errorf("HEAD~1 and HEAD~2 should be in protected, got %+v", got)
	}
}

func TestCommitsContainedInUnknownRef(t *testing.T) {
	r := testRepo(t, 1)
	ctx := context.Background()
	sha, _ := r.RevParse(ctx, "HEAD")
	got, err := r.CommitsContainedIn(ctx, "origin/does-not-exist", []string{sha})
	if err != nil {
		t.Fatalf("CommitsContainedIn unknown ref: %v", err)
	}
	if len(got) != 0 && got[sha] {
		t.Errorf("expected no commits reported as contained, got %+v", got)
	}
}
