package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
)

// ccPrepareMain is invoked as `commit-composer __cc-prepare [--hint=...] [-C DIR]`.
//
// It is the single pre-flight + prepare step for the /cc-commit fast path: it
// checks we are in a git repo, checks whether the working tree is dirty, and -
// when dirty - synthesizes the minimal WORKING recompose plan, runs the
// split-prepare analysis, and prints machine-readable KEY=value lines the slash
// command consumes. Doing this in the binary means the slash command issues a
// single `commit-composer __cc-prepare` call with no shell command-substitution
// (`$(mktemp ...)`, heredocs) of its own - so user shell-approval hooks stay
// quiet and one allow-list entry covers it.
//
// Output:
//
//	DIRTY=no                              # clean tree; caller falls back to compose
//	DIRTY=yes
//	PLAN_FILE=/tmp/cc-commit-plan-...      # synthesized WORKING plan
//	SPLITS_DIR=/tmp/cc-commit-splits-...
//	FILES=/tmp/cc-commit-splits-.../WORKING.files.txt
//	--- BEGIN FILES ---                    # inline copy of FILES, see printFileList
//	M	internal/git/git.go
//	A	docs/new.md
//	--- END FILES ---
//
// Temp files are intentionally left on disk (the user inspects them after a
// run; see the no-auto-rm rule in the slash command).
func ccPrepareMain(args []string) error {
	fs := flag.NewFlagSet("__cc-prepare", flag.ContinueOnError)
	dir := fs.String("C", "", "run as if started in this directory")
	// --hint is accepted for symmetry with the slash command's argument and
	// possible future use; grouping is Claude's job, so the binary only needs
	// to tolerate the flag being passed.
	_ = fs.String("hint", "", "optional free-text grouping hint (unused by the binary)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	repo := git.Repo{Dir: *dir}
	ctx, cancel := prepareContext()
	defer cancel()

	if _, err := repo.Run(ctx, "rev-parse", "--git-dir"); err != nil {
		return errors.New("not in a git repository")
	}

	clean, err := repo.IsClean(ctx)
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if clean {
		fmt.Println("DIRTY=no")
		return nil
	}

	planFile, err := os.CreateTemp("", "cc-commit-plan-")
	if err != nil {
		return fmt.Errorf("create plan file: %w", err)
	}
	wtPlan := plan.Plan{
		Base: git.UncommittedSHA,
		Ops:  []plan.Op{{SHA: git.UncommittedSHA, Action: plan.ClaudeRecompose}},
	}
	if _, err := planFile.WriteString(plan.Marshal(wtPlan)); err != nil {
		planFile.Close()
		return fmt.Errorf("write plan: %w", err)
	}
	if err := planFile.Close(); err != nil {
		return fmt.Errorf("close plan: %w", err)
	}

	splitsDir, err := os.MkdirTemp("", "cc-commit-splits-")
	if err != nil {
		return fmt.Errorf("create splits dir: %w", err)
	}
	if err := runSplitPrepare(ctx, planFile.Name(), splitsDir, *dir); err != nil {
		return err
	}

	filesPath := filepath.Join(splitsDir, git.UncommittedSHA+".files.txt")
	fmt.Printf("DIRTY=yes\nPLAN_FILE=%s\nSPLITS_DIR=%s\nFILES=%s\n",
		planFile.Name(), splitsDir, filesPath)
	printFileList(filesPath)
	return nil
}

// maxInlineFiles caps the inline name-status dump. The list is echoed so the
// /cc-commit skill can decide its commit grouping straight from this command's
// output; past a few hundred files that stops being a saving and turns into a
// context dump, so the caller reads FILES itself instead.
const maxInlineFiles = 300

// printFileList echoes the name-status lines between markers, so a caller that
// injects this command's output into a prompt already has the file list and does
// not need a follow-up read. Best-effort: FILES stays on disk either way, and
// the markers are omitted entirely when it cannot be read.
func printFileList(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	body := strings.TrimRight(string(data), "\n")
	if body == "" {
		return
	}
	lines := strings.Split(body, "\n")
	if len(lines) > maxInlineFiles {
		// No markers - their absence is the signal to read FILES.
		fmt.Printf("FILES_TRUNCATED=%d\n", len(lines))
		return
	}
	fmt.Println("--- BEGIN FILES ---")
	for _, line := range lines {
		fmt.Println(line)
	}
	fmt.Println("--- END FILES ---")
}
