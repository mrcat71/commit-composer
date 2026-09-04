package git

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mrcat71/commit-composer/internal/plan"
)

func TestTrimLeadingPicks(t *testing.T) {
	mkOps := func(actions ...plan.Action) []plan.Op {
		ops := make([]plan.Op, len(actions))
		for i, a := range actions {
			ops[i] = plan.Op{
				SHA:       fmt.Sprintf("sha%02d", i),
				Action:    a,
				OrigIndex: i,
			}
		}
		return ops
	}
	tests := []struct {
		name     string
		in       plan.Plan
		wantBase string
		wantOps  int
	}{
		{
			name: "no trim when first op is non-pick",
			in: plan.Plan{
				Base: "base0",
				Ops:  mkOps(plan.Reword, plan.Pick, plan.Pick),
			},
			wantBase: "base0",
			wantOps:  3,
		},
		{
			name: "trim 3 leading picks, keep recompose tail",
			in: plan.Plan{
				Base: "base0",
				Ops:  mkOps(plan.Pick, plan.Pick, plan.Pick, plan.ClaudeRecompose, plan.ClaudeRecompose),
			},
			wantBase: "sha02", // SHA of the last leading pick
			wantOps:  2,
		},
		{
			name: "stop trimming at first non-pick",
			in: plan.Plan{
				Base: "base0",
				Ops:  mkOps(plan.Pick, plan.Pick, plan.Squash, plan.Pick),
			},
			wantBase: "sha01",
			wantOps:  2,
		},
		{
			name: "reorder prevents trimming",
			in: plan.Plan{
				Base: "base0",
				Ops: []plan.Op{
					{SHA: "a", Action: plan.Pick, OrigIndex: 1},
					{SHA: "b", Action: plan.Pick, OrigIndex: 0},
					{SHA: "c", Action: plan.Reword, OrigIndex: 2, NewMessage: "x"},
				},
			},
			wantBase: "base0",
			wantOps:  3,
		},
		{
			name: "all picks pass-through trims to empty",
			in: plan.Plan{
				Base: "base0",
				Ops:  mkOps(plan.Pick, plan.Pick, plan.Pick),
			},
			wantBase: "sha02",
			wantOps:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TrimLeadingPicks(tc.in)
			if got.Base != tc.wantBase {
				t.Errorf("Base: got %q want %q", got.Base, tc.wantBase)
			}
			if len(got.Ops) != tc.wantOps {
				t.Errorf("Ops len: got %d want %d", len(got.Ops), tc.wantOps)
			}
		})
	}
}

func TestBuildTodo(t *testing.T) {
	p := plan.Plan{
		Base: "deadbeef",
		Ops: []plan.Op{
			{SHA: "aaa", Action: plan.Pick},
			{SHA: "bbb", Action: plan.Squash},
			{SHA: "ccc", Action: plan.Reword, NewMessage: "new msg"},
			{SHA: "ddd", Action: plan.Drop},
			{SHA: "eee", Action: plan.Edit},
			{SHA: "fff", Action: plan.Fixup},
		},
	}
	subs := map[string]string{"aaa": "subj-a", "bbb": "subj-b", "ccc": "subj-c", "ddd": "subj-d", "eee": "subj-e", "fff": "subj-f"}
	got := BuildTodo(p, subs)
	want := "" +
		"pick aaa subj-a\n" +
		"squash bbb subj-b\n" +
		"reword ccc subj-c\n" +
		"drop ddd subj-d\n" +
		"edit eee subj-e\n" +
		"fixup fff subj-f\n"
	if got != want {
		t.Errorf("BuildTodo mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRewordMessages(t *testing.T) {
	p := plan.Plan{
		Ops: []plan.Op{
			{SHA: "aaa", Action: plan.Pick},
			{SHA: "bbb", Action: plan.Reword, NewMessage: "rewritten b"},
			{SHA: "ccc", Action: plan.Reword}, // empty - skipped
			{SHA: "ddd", Action: plan.Reword, NewMessage: "rewritten d"},
		},
	}
	got := RewordMessages(p)
	if got["aaa"] != "" {
		t.Errorf("aaa: got %q want empty", got["aaa"])
	}
	if got["bbb"] != "rewritten b" || got["ddd"] != "rewritten d" {
		t.Errorf("got %+v", got)
	}
	if _, ok := got["ccc"]; ok {
		t.Errorf("empty-message reword should be skipped, got %q", got["ccc"])
	}
}

// errGoToolchainMissing marks the one build failure that should skip rather
// than fail: no go binary on PATH.
var errGoToolchainMissing = errors.New("go toolchain not available")

// sharedBinDir is the directory sharedBinary built into, kept so TestMain can
// remove it once every test has finished with the binary.
var sharedBinDir string

// sharedBinary compiles the commit-composer binary once per package run and
// hands the same path to every caller. The end-to-end tests below each drive
// the real binary as a subprocess; building it per test cost a link and a
// process spawn ten times over and dominated the whole suite's runtime.
var sharedBinary = sync.OnceValues(func() (string, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", errGoToolchainMissing
	}
	dir, err := os.MkdirTemp("", "commit-composer-test-bin")
	if err != nil {
		return "", fmt.Errorf("temp dir for test binary: %w", err)
	}
	sharedBinDir = dir
	bin := filepath.Join(dir, "commit-composer")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/mrcat71/commit-composer/cmd/commit-composer").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("go build commit-composer: %w\n%s", err, out)
	}
	return bin, nil
})

func TestMain(m *testing.M) {
	code := m.Run()
	// Safe to read without synchronisation: every write happened inside the
	// sync.OnceValues call, and m.Run has returned, so all tests are done.
	if sharedBinDir != "" {
		_ = os.RemoveAll(sharedBinDir)
	}
	os.Exit(code)
}

// buildSelf returns the path to the commit-composer binary, compiling it on the
// first call. Skips the test if go is missing.
func buildSelf(t *testing.T) string {
	t.Helper()
	bin, err := sharedBinary()
	if errors.Is(err, errGoToolchainMissing) {
		t.Skip("go toolchain not available")
	}
	if err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestApplyEndToEndClaudeSplit verifies that a ClaudeSplit op pauses the
// rebase as an `edit`, finds its <sha>.split.json, and executes the
// pre-approved split into two commits.
func TestApplyEndToEndClaudeSplit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 2) // c1, c2
	ctx := context.Background()

	// Make a third commit that touches TWO files - this is the one we will split.
	if err := os.WriteFile(filepath.Join(r.Dir, "auth.go"), []byte("package x\nfunc Auth(){}\n"), 0o644); err != nil {
		t.Fatalf("write auth.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "docs.md"), []byte("# docs\n"), 0o644); err != nil {
		t.Fatalf("write docs.md: %v", err)
	}
	if _, err := r.Run(ctx, "add", "auth.go", "docs.md"); err != nil {
		t.Fatalf("git add mixed: %v", err)
	}
	if _, err := r.Run(ctx, "commit", "-q", "-m", "c3 mixed: auth + docs"); err != nil {
		t.Fatalf("commit c3: %v", err)
	}

	base, head, _, err := r.ResolveRange(ctx, "HEAD~1")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("expected 1 commit in range, got %d", len(commits))
	}
	splitSHA := commits[0].SHA

	// Pre-approved split: auth.go -> "feat: add Auth", docs.md -> "docs: add page".
	splitsDir := t.TempDir()
	spec := SplitSpec{
		SHA:      splitSHA,
		PoolSize: 1,
		Groups: []SplitGroup{
			{Files: []string{"auth.go"}, Message: "feat: add Auth"},
			{Files: []string{"docs.md"}, Message: "docs: add page"},
		},
	}
	specBytes, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(splitsDir, splitSHA+".split.json"), specBytes, 0o600); err != nil {
		t.Fatalf("write split spec: %v", err)
	}

	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: splitSHA, Action: plan.ClaudeRecompose, OrigIndex: 0},
		},
	}

	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe:   self,
		SplitsDir: splitsDir,
		Stdout:    os.Stderr,
		Stderr:    os.Stderr,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// History should be c1, c2, feat..., docs...
	logOut, err := r.Run(ctx, "log", "--reverse", "--format=%s")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "c2", "feat: add Auth", "docs: add page"}, "\n")
	if got != want {
		t.Fatalf("post-rebase log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Verify the per-commit file scoping.
	checkOnly := func(rev, file string) {
		out, err := r.Run(ctx, "show", "--name-only", "--format=", rev)
		if err != nil {
			t.Fatalf("git show %s: %v", rev, err)
		}
		names := strings.Fields(strings.TrimSpace(out))
		if len(names) != 1 || names[0] != file {
			t.Errorf("%s touches files=%v, want exactly [%s]", rev, names, file)
		}
	}
	checkOnly("HEAD", "docs.md")
	checkOnly("HEAD~1", "auth.go")
}

// TestApplyClaudeSplitMissingJSONFailsEarly ensures Apply refuses to start when
// a claude-split op has no matching JSON.
// TestApplyClaudeRecomposePool covers a multi-commit pool: 3 consecutive
// claude-recompose commits dissolved into 2 logical commits.
func TestApplyClaudeRecomposePool(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 1) // c1
	ctx := context.Background()

	// Create 3 commits forming a pool. The pool will be redesigned into 2.
	mk := func(name, body, msg string) {
		if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := r.Run(ctx, "add", name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		if _, err := r.Run(ctx, "commit", "-q", "-m", msg); err != nil {
			t.Fatalf("commit %s: %v", name, err)
		}
	}
	mk("auth.go", "package x\nfunc Auth(){}\n", "wip auth start")
	mk("auth_test.go", "package x\n", "wip auth test")
	mk("docs.md", "# docs\n", "wip docs")

	base, _, _, err := r.ResolveRange(ctx, "HEAD~3")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}

	splitsDir := t.TempDir()
	lastSHA := commits[2].SHA
	spec := SplitSpec{
		SHA:      lastSHA,
		PoolSize: 3,
		Groups: []SplitGroup{
			{Files: []string{"auth.go", "auth_test.go"}, Message: "feat: Auth helper + test"},
			{Files: []string{"docs.md"}, Message: "docs: explain auth"},
		},
	}
	specBytes, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(splitsDir, lastSHA+".split.json"), specBytes, 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[0].SHA, Action: plan.ClaudeRecompose, OrigIndex: 0},
			{SHA: commits[1].SHA, Action: plan.ClaudeRecompose, OrigIndex: 1},
			{SHA: commits[2].SHA, Action: plan.ClaudeRecompose, OrigIndex: 2},
		},
	}
	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe:   self,
		SplitsDir: splitsDir,
		Stdout:    os.Stderr,
		Stderr:    os.Stderr,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	logOut, _ := r.Run(ctx, "log", "--reverse", "--format=%s")
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "feat: Auth helper + test", "docs: explain auth"}, "\n")
	if got != want {
		t.Fatalf("post-recompose log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestApplyUncommittedOnly exercises the new working-tree recompose path.
// No commit-level ops in the plan; uncommitted files get committed in
// groups according to a pre-written WORKING.split.json.
func TestApplyUncommittedOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 1) // c1, then we'll dirty the tree
	ctx := context.Background()

	// Dirty the working tree with two unrelated changes.
	if err := os.WriteFile(filepath.Join(r.Dir, "auth.go"), []byte("package x\nfunc Auth(){}\n"), 0o644); err != nil {
		t.Fatalf("write auth.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "docs.md"), []byte("# docs\n"), 0o644); err != nil {
		t.Fatalf("write docs.md: %v", err)
	}

	splitsDir := t.TempDir()
	spec := SplitSpec{
		SHA: UncommittedSHA,
		Groups: []SplitGroup{
			{Files: []string{"auth.go"}, Message: "feat: add Auth"},
			{Files: []string{"docs.md"}, Message: "docs: explain Auth"},
		},
	}
	specBytes, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(splitsDir, UncommittedSHA+".split.json"), specBytes, 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	headSHA, _ := r.RevParse(ctx, "HEAD")
	p := plan.Plan{
		Base: headSHA, // no rebase ops; Base is just for record-keeping
		Ops: []plan.Op{
			{SHA: UncommittedSHA, Action: plan.ClaudeRecompose, OrigIndex: -1},
		},
	}
	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe:   self,
		SplitsDir: splitsDir,
		Stdout:    os.Stderr,
		Stderr:    os.Stderr,
	}); err != nil {
		t.Fatalf("Apply uncommitted-only: %v", err)
	}

	// History should be c1, feat..., docs...
	logOut, _ := r.Run(ctx, "log", "--reverse", "--format=%s")
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "feat: add Auth", "docs: explain Auth"}, "\n")
	if got != want {
		t.Fatalf("post-commit log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Working tree must be clean now.
	clean, _ := r.IsClean(ctx)
	if !clean {
		t.Error("expected clean working tree after uncommitted recompose")
	}
}

// TestApplyUncommittedHunks exercises hunk-level groups for the WORKING
// pool. Two hunks in the same modified file are split into two commits
// using SplitGroup.Hunks indices, mirroring the line-level scope that
// executeSplit already supports for real commit pools.
func TestApplyUncommittedHunks(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 1)
	ctx := context.Background()

	// Seed a tracked file with enough lines that two edits land in
	// separate hunks (need >6 unchanged lines between them).
	seed := strings.Join([]string{
		"line 01", "line 02", "line 03", "line 04", "line 05",
		"line 06", "line 07", "line 08", "line 09", "line 10",
		"line 11", "line 12", "line 13", "line 14", "line 15",
		"line 16", "line 17", "line 18", "line 19", "line 20",
	}, "\n") + "\n"
	path := filepath.Join(r.Dir, "data.txt")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if _, err := r.Run(ctx, "add", "data.txt"); err != nil {
		t.Fatalf("git add seed: %v", err)
	}
	if _, err := r.Run(ctx, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit seed: %v", err)
	}

	// Dirty the tree: edit line 2 AND line 19 — gaps wide enough that
	// `git diff` emits two hunks.
	lines := strings.Split(strings.TrimRight(seed, "\n"), "\n")
	lines[1] = "line 02 EDITED-A"
	lines[18] = "line 19 EDITED-B"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	// Sanity check: the working diff has at least two hunks we can index.
	diff, err := r.UncommittedDiff(ctx)
	if err != nil {
		t.Fatalf("UncommittedDiff: %v", err)
	}
	hs, err := ParseHunks(diff)
	if err != nil {
		t.Fatalf("ParseHunks: %v", err)
	}
	if len(hs) < 2 {
		t.Fatalf("expected at least 2 hunks, got %d:\n%s", len(hs), diff)
	}

	splitsDir := t.TempDir()
	spec := SplitSpec{
		SHA: UncommittedSHA,
		Groups: []SplitGroup{
			{Hunks: []int{0}, Message: "edit: tweak line 02"},
			{Hunks: []int{1}, Message: "edit: tweak line 19"},
		},
	}
	specBytes, _ := json.Marshal(spec)
	if err := os.WriteFile(filepath.Join(splitsDir, UncommittedSHA+".split.json"), specBytes, 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	headSHA, _ := r.RevParse(ctx, "HEAD")
	p := plan.Plan{
		Base: headSHA,
		Ops:  []plan.Op{{SHA: UncommittedSHA, Action: plan.ClaudeRecompose, OrigIndex: -1}},
	}
	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe:   self,
		SplitsDir: splitsDir,
		Stdout:    os.Stderr,
		Stderr:    os.Stderr,
	}); err != nil {
		t.Fatalf("Apply uncommitted hunks: %v", err)
	}

	// History should be: c1, seed, edit A, edit B.
	logOut, _ := r.Run(ctx, "log", "--reverse", "--format=%s")
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "seed", "edit: tweak line 02", "edit: tweak line 19"}, "\n")
	if got != want {
		t.Fatalf("post-commit log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// Working tree must be clean.
	clean, _ := r.IsClean(ctx)
	if !clean {
		out, _ := r.Run(ctx, "status", "--porcelain")
		t.Fatalf("expected clean tree, status:\n%s", out)
	}
}

func TestApplyClaudeSplitMissingJSONFailsEarly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 2)
	ctx := context.Background()
	headSHA, _ := r.RevParse(ctx, "HEAD")

	p := plan.Plan{
		Base: "deadbeef", // unused - validate catches the missing JSON first
		Ops: []plan.Op{
			{SHA: headSHA, Action: plan.ClaudeRecompose, OrigIndex: 0},
		},
	}
	self := buildSelf(t)
	err := r.Apply(ctx, p, ApplyOptions{
		SelfExe:   self,
		SplitsDir: t.TempDir(), // empty dir
		Stdout:    os.Stderr,
		Stderr:    os.Stderr,
	})
	if err == nil {
		t.Fatal("expected error for missing split JSON, got nil")
	}
	if !strings.Contains(err.Error(), "missing recompose JSON") {
		t.Errorf("error should mention missing recompose JSON, got: %v", err)
	}
}

// TestApplyEndToEndPureSquash verifies the squash action: two commits get
// combined into one, both messages preserved (concatenated by git's default
// COMMIT_EDITMSG template), no commit-composer staged reword needed.
func TestApplyEndToEndPureSquash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 3) // c1, c2, c3
	ctx := context.Background()
	base, _, _, err := r.ResolveRange(ctx, "HEAD~2")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	// Squash c3 into c2.
	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[0].SHA, Action: plan.Pick, OrigIndex: 0},
			{SHA: commits[1].SHA, Action: plan.Squash, OrigIndex: 1},
		},
	}
	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe: self,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	}); err != nil {
		t.Fatalf("Apply (squash): %v", err)
	}
	// Should now have 2 commits total (c1 + squashed c2c3).
	out, _ := r.Run(ctx, "log", "--reverse", "--format=%s", "--no-merges")
	subjects := strings.Split(strings.TrimSpace(out), "\n")
	if len(subjects) != 2 {
		t.Fatalf("expected 2 commits after squash, got %d: %v", len(subjects), subjects)
	}
	if subjects[0] != "c1" {
		t.Errorf("first commit subject: got %q want c1", subjects[0])
	}
	// The combined commit's subject is git's default which keeps c2's first
	// line. Either form is fine - assert it mentions c2.
	if !strings.Contains(subjects[1], "c2") {
		t.Errorf("squashed commit subject should mention c2, got %q", subjects[1])
	}
	// Both files must be in the squashed commit.
	files, _ := r.Run(ctx, "show", "--name-only", "--format=", "HEAD")
	names := strings.Fields(strings.TrimSpace(files))
	if !contains(names, "f2.txt") || !contains(names, "f3.txt") {
		t.Errorf("squashed commit should touch both f2.txt and f3.txt, got %v", names)
	}
}

// TestApplyEndToEndEditPausesForUser verifies that an `edit` action pauses
// the rebase and Apply returns an error pointing the user at `git rebase
// --continue`. The rebase-merge dir should remain in place.
func TestApplyEndToEndEditPausesForUser(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 3)
	ctx := context.Background()
	base, _, _, err := r.ResolveRange(ctx, "HEAD~2")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[0].SHA, Action: plan.Edit, OrigIndex: 0},
			{SHA: commits[1].SHA, Action: plan.Pick, OrigIndex: 1},
		},
	}
	self := buildSelf(t)
	err = r.Apply(ctx, p, ApplyOptions{
		SelfExe: self,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	})
	if err == nil {
		t.Fatal("expected Apply to return error when paused on edit, got nil")
	}
	if !strings.Contains(err.Error(), "manual edit") {
		t.Errorf("error should mention manual edit, got: %v", err)
	}
	// The rebase should still be in progress so the user can continue it.
	inProgress, err := r.RebaseInProgress(ctx)
	if err != nil {
		t.Fatalf("RebaseInProgress: %v", err)
	}
	if !inProgress {
		t.Errorf("expected rebase to still be in progress after edit pause")
	}
	// Clean up so subsequent tests don't see a dangling rebase state.
	if _, err := r.Run(ctx, "rebase", "--abort"); err != nil {
		t.Logf("rebase --abort (cleanup): %v", err)
	}
}

// TestApplyEndToEndReorder verifies that swapping two adjacent picks in the
// plan reorders the commits in the new history.
func TestApplyEndToEndReorder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 4) // c1, c2, c3, c4
	ctx := context.Background()
	// Touch different files so c2 and c3 don't conflict when reordered.
	base, _, _, err := r.ResolveRange(ctx, "HEAD~3")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, "HEAD")
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}
	// Swap commits[0] (c2) and commits[1] (c3) in the plan.
	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[1].SHA, Action: plan.Pick, OrigIndex: 1},
			{SHA: commits[0].SHA, Action: plan.Pick, OrigIndex: 0},
			{SHA: commits[2].SHA, Action: plan.Pick, OrigIndex: 2},
		},
	}
	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe: self,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	}); err != nil {
		t.Fatalf("Apply (reorder): %v", err)
	}
	// New history should be c1, c3, c2, c4.
	out, _ := r.Run(ctx, "log", "--reverse", "--format=%s", "--no-merges")
	got := strings.TrimSpace(out)
	want := strings.Join([]string{"c1", "c3", "c2", "c4"}, "\n")
	if got != want {
		t.Fatalf("post-reorder log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func TestApplyEndToEndSquash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 4)
	ctx := context.Background()
	base, head, _, err := r.ResolveRange(ctx, "HEAD~3")
	if err != nil {
		t.Fatalf("ResolveRange: %v", err)
	}
	commits, err := r.Log(ctx, base, head)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	_ = head

	// pick c2, fixup c3 into c2, pick c4. We use fixup (not squash) so the
	// rebase doesn't drop us into the editor for a combined message.
	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[0].SHA, Action: plan.Pick, OrigIndex: 0},
			{SHA: commits[1].SHA, Action: plan.Fixup, OrigIndex: 1},
			{SHA: commits[2].SHA, Action: plan.Pick, OrigIndex: 2},
		},
	}

	self := buildSelf(t)
	if err := r.Apply(ctx, p, ApplyOptions{
		SelfExe: self,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// History should now be c1, c2, c4 - c3's tree changes folded into c2.
	logOut, err := r.Run(ctx, "log", "--reverse", "--format=%s")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "c2", "c4"}, "\n")
	if got != want {
		t.Fatalf("post-rebase log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// f3.txt should exist (its creation folded into c2).
	if _, err := os.Stat(filepath.Join(r.Dir, "f3.txt")); err != nil {
		t.Fatalf("expected f3.txt to exist after fixup: %v", err)
	}
}

func TestApplyEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Parallel()
	r := testRepo(t, 4) // c1, c2, c3, c4
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
		t.Fatalf("expected 3 commits, got %d", len(commits))
	}

	// Build a plan: pick c2, drop c3, pick c4 (and reword c4).
	p := plan.Plan{
		Base: base,
		Ops: []plan.Op{
			{SHA: commits[0].SHA, Action: plan.Pick, OrigIndex: 0},
			{SHA: commits[1].SHA, Action: plan.Drop, OrigIndex: 1},
			{SHA: commits[2].SHA, Action: plan.Reword, NewMessage: "c4 reworded", OrigIndex: 2},
		},
	}

	self := buildSelf(t)
	err = r.Apply(ctx, p, ApplyOptions{
		SelfExe: self,
		Stdout:  os.Stderr,
		Stderr:  os.Stderr,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Verify history is now c1, c2, c4(reworded).
	logOut, err := r.Run(ctx, "log", "--reverse", "--format=%s", "--no-merges")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	got := strings.TrimSpace(logOut)
	want := strings.Join([]string{"c1", "c2", "c4 reworded"}, "\n")
	if got != want {
		t.Fatalf("post-rebase log mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// HEAD should not be c3.
	files, err := r.Run(ctx, "log", "--name-only", "--format=", "-1", "HEAD")
	_ = files
	_ = head
	if err != nil {
		t.Fatalf("log HEAD: %v", err)
	}
}
