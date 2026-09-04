package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrcat71/commit-composer/internal/git"
)

// gitRepo builds a throwaway git repo with a single "base" commit and returns
// its path. Skips the test when git is unavailable.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "test@example.invalid")
	run("config", "user.name", "Test User")
	run("config", "commit.gpgsign", "false")
	writeFile(t, dir, "base.txt", "base\n")
	run("add", "base.txt")
	run("commit", "-q", "-m", "chore: base")
	return dir
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// captureStdout redirects os.Stdout to a temp file for the duration of fn and
// returns what was written. A temp file (not a pipe) avoids buffer deadlocks
// on large output.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	f, err := os.CreateTemp(t.TempDir(), "stdout-")
	if err != nil {
		t.Fatalf("create temp stdout: %v", err)
	}
	os.Stdout = f
	callErr := fn()
	os.Stdout = old
	f.Close()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(data), callErr
}

// parseKV turns "KEY=value" lines into a map. Values may contain '='.
func parseKV(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if i := strings.IndexByte(line, '='); i > 0 {
			m[line[:i]] = line[i+1:]
		}
	}
	return m
}

func TestCcPrepareCleanTree(t *testing.T) {
	dir := gitRepo(t)
	out, err := captureStdout(t, func() error {
		return ccPrepareMain([]string{"-C", dir})
	})
	if err != nil {
		t.Fatalf("ccPrepareMain: %v", err)
	}
	if strings.TrimSpace(out) != "DIRTY=no" {
		t.Errorf("clean tree: want %q, got %q", "DIRTY=no", strings.TrimSpace(out))
	}
}

func TestCcPrepareDirtyTree(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "new.go", "package main\n")

	out, err := captureStdout(t, func() error {
		return ccPrepareMain([]string{"-C", dir})
	})
	if err != nil {
		t.Fatalf("ccPrepareMain: %v", err)
	}
	kv := parseKV(out)
	if kv["DIRTY"] != "yes" {
		t.Fatalf("want DIRTY=yes, got output:\n%s", out)
	}
	for _, key := range []string{"PLAN_FILE", "SPLITS_DIR", "FILES"} {
		if kv[key] == "" {
			t.Errorf("missing %s in output:\n%s", key, out)
		}
	}

	// The synthesized plan must parse and carry the WORKING recompose op.
	pf, err := os.ReadFile(kv["PLAN_FILE"])
	if err != nil {
		t.Fatalf("read plan file: %v", err)
	}
	if !strings.Contains(string(pf), "claude-recompose "+git.UncommittedSHA) {
		t.Errorf("plan missing WORKING recompose op:\n%s", pf)
	}

	// The files list must name the dirty file.
	fl, err := os.ReadFile(kv["FILES"])
	if err != nil {
		t.Fatalf("read files list: %v", err)
	}
	if !strings.Contains(string(fl), "new.go") {
		t.Errorf("files list missing new.go:\n%s", fl)
	}

	// __split-prepare should have written the WORKING diff too.
	if _, err := os.Stat(filepath.Join(kv["SPLITS_DIR"], git.UncommittedSHA+".diff")); err != nil {
		t.Errorf("WORKING.diff not written: %v", err)
	}

	// The file list must also be echoed inline between markers - the /cc-commit
	// skill injects this output at load time and groups from it without a
	// follow-up Read.
	inline, ok := inlineFiles(out)
	if !ok {
		t.Fatalf("missing --- BEGIN/END FILES --- markers in output:\n%s", out)
	}
	if !strings.Contains(inline, "new.go") {
		t.Errorf("inline files block missing new.go:\n%s", inline)
	}
	if inline != strings.TrimRight(string(fl), "\n") {
		t.Errorf("inline files block differs from FILES contents:\ninline: %q\nfile:   %q", inline, string(fl))
	}
}

// inlineFiles extracts the body between __cc-prepare's file-list markers.
func inlineFiles(out string) (string, bool) {
	_, rest, ok := strings.Cut(out, "--- BEGIN FILES ---\n")
	if !ok {
		return "", false
	}
	body, _, ok := strings.Cut(rest, "\n--- END FILES ---")
	return body, ok
}

func TestCcPrepareInlineFilesTruncated(t *testing.T) {
	dir := gitRepo(t)
	for i := range maxInlineFiles + 1 {
		writeFile(t, dir, fmt.Sprintf("f%03d.txt", i), "x\n")
	}

	out, err := captureStdout(t, func() error {
		return ccPrepareMain([]string{"-C", dir})
	})
	if err != nil {
		t.Fatalf("ccPrepareMain: %v", err)
	}
	if _, ok := inlineFiles(out); ok {
		t.Error("file list should not be inlined past maxInlineFiles")
	}
	if got := parseKV(out)["FILES_TRUNCATED"]; got == "" {
		t.Errorf("want FILES_TRUNCATED marker, got:\n%s", out)
	}
	// The path itself must still be usable when the inline copy is suppressed.
	if _, err := os.Stat(parseKV(out)["FILES"]); err != nil {
		t.Errorf("FILES path unusable: %v", err)
	}
}

func TestCcPrepareNotGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir() // not a git repo
	_, err := captureStdout(t, func() error {
		return ccPrepareMain([]string{"-C", dir})
	})
	if err == nil {
		t.Fatal("expected error outside a git repo, got nil")
	}
	if !strings.Contains(err.Error(), "not in a git repository") {
		t.Errorf("want 'not in a git repository', got: %v", err)
	}
}

func TestResolveLauncher(t *testing.T) {
	// Neutralize ambient env so the test is deterministic.
	t.Setenv("COMMIT_COMPOSER_LAUNCHER", "")
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("CLAUDE_PLUGIN_DATA", "")

	// writeLauncher creates an executable launcher at <root>/<sub...>/ and
	// returns its path.
	writeLauncher := func(t *testing.T, root string, sub ...string) string {
		t.Helper()
		dir := filepath.Join(append([]string{root}, sub...)...)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(dir, launcherScript)
		if err := os.WriteFile(path, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
			t.Fatalf("write script: %v", err)
		}
		return path
	}

	root := t.TempDir()
	scriptPath := writeLauncher(t, root, "scripts")

	t.Run("via plugin-root", func(t *testing.T) {
		got, err := resolveLauncher(root)
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != scriptPath {
			t.Errorf("want %q, got %q", scriptPath, got)
		}
	})

	t.Run("via CLAUDE_PLUGIN_ROOT", func(t *testing.T) {
		envRoot := t.TempDir()
		want := writeLauncher(t, envRoot, "scripts")
		t.Setenv("CLAUDE_PLUGIN_ROOT", envRoot)
		got, err := resolveLauncher("")
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})

	// Pre-0.4 plugin trees kept scripts under .claude-plugin/. A user running a
	// cached copy of one must still launch.
	t.Run("legacy .claude-plugin layout still resolves", func(t *testing.T) {
		legacyRoot := t.TempDir()
		want := writeLauncher(t, legacyRoot, ".claude-plugin", "scripts")
		got, err := resolveLauncher(legacyRoot)
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != want {
			t.Errorf("want %q, got %q", want, got)
		}
	})

	t.Run("current layout wins over legacy", func(t *testing.T) {
		bothRoot := t.TempDir()
		writeLauncher(t, bothRoot, ".claude-plugin", "scripts")
		want := writeLauncher(t, bothRoot, "scripts")
		got, err := resolveLauncher(bothRoot)
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != want {
			t.Errorf("want current layout %q, got %q", want, got)
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		override := filepath.Join(t.TempDir(), "custom.sh")
		if err := os.WriteFile(override, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
			t.Fatalf("write override: %v", err)
		}
		t.Setenv("COMMIT_COMPOSER_LAUNCHER", override)
		got, err := resolveLauncher(root)
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != override {
			t.Errorf("env override should win: want %q, got %q", override, got)
		}
	})

	t.Run("CLAUDE_PLUGIN_DATA override", func(t *testing.T) {
		dataRoot := t.TempDir()
		dataScripts := filepath.Join(dataRoot, "scripts")
		if err := os.MkdirAll(dataScripts, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		dataScript := filepath.Join(dataScripts, launcherScript)
		if err := os.WriteFile(dataScript, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
		t.Setenv("CLAUDE_PLUGIN_DATA", dataRoot)
		got, err := resolveLauncher(root)
		if err != nil {
			t.Fatalf("resolveLauncher: %v", err)
		}
		if got != dataScript {
			t.Errorf("CLAUDE_PLUGIN_DATA should win over plugin-root: want %q, got %q", dataScript, got)
		}
	})

	t.Run("non-executable is skipped", func(t *testing.T) {
		plainRoot := t.TempDir()
		plain := filepath.Join(plainRoot, "scripts")
		if err := os.MkdirAll(plain, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(plain, launcherScript), []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := resolveLauncher(plainRoot); err == nil {
			t.Error("expected error when only a non-executable launcher exists")
		}
	})
}

func TestSplitPrepareOptionalOut(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "x.txt", "x\n")

	// Synthesize a WORKING plan so __split-prepare has a pool to analyze.
	planPath := filepath.Join(t.TempDir(), "plan.txt")
	wtPlan := "## commit-composer plan v1\nbase: " + git.UncommittedSHA + "\nops:\n- claude-recompose " + git.UncommittedSHA + "\n"
	if err := os.WriteFile(planPath, []byte(wtPlan), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return splitPrepareMain([]string{"--plan=" + planPath, "-C", dir})
	})
	if err != nil {
		t.Fatalf("splitPrepareMain (no --out): %v", err)
	}
	kv := parseKV(out)
	if kv["SPLITS_DIR"] == "" {
		t.Fatalf("expected SPLITS_DIR line when --out omitted, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(kv["SPLITS_DIR"], "manifest.json")); err != nil {
		t.Errorf("manifest.json not written to temp splits dir: %v", err)
	}
}

func TestRunApplyWorkingSummary(t *testing.T) {
	dir := gitRepo(t)
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "b.txt", "b\n")

	planPath := filepath.Join(t.TempDir(), "plan.txt")
	wtPlan := "## commit-composer plan v1\nbase: " + git.UncommittedSHA + "\nops:\n- claude-recompose " + git.UncommittedSHA + "\n"
	if err := os.WriteFile(planPath, []byte(wtPlan), 0o600); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	splitsDir := t.TempDir()
	split := `{"sha":"WORKING","pool_size":0,"groups":[` +
		`{"files":["a.txt"],"message":"feat: add a"},` +
		`{"files":["b.txt"],"message":"feat: add b"}]}`
	if err := os.WriteFile(filepath.Join(splitsDir, git.UncommittedSHA+".split.json"), []byte(split), 0o600); err != nil {
		t.Fatalf("write split: %v", err)
	}

	out, err := captureStdout(t, func() error {
		return runApply(context.Background(), git.Repo{Dir: dir}, planPath, splitsDir)
	})
	if err != nil {
		t.Fatalf("runApply: %v", err)
	}

	var committed []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "COMMITTED ") {
			committed = append(committed, line)
		}
	}
	if len(committed) != 2 {
		t.Fatalf("want 2 COMMITTED lines, got %d:\n%s", len(committed), out)
	}
	if !strings.Contains(committed[0], "feat: add a") || !strings.Contains(committed[1], "feat: add b") {
		t.Errorf("COMMITTED lines wrong order/content:\n%s", strings.Join(committed, "\n"))
	}

	// The working tree must be clean after apply (both files committed).
	clean, err := git.Repo{Dir: dir}.IsClean(context.Background())
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Error("working tree not clean after WORKING apply")
	}
}
