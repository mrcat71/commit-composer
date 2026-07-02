package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
)

// launcherScript is the terminal-overlay dispatcher shipped next to the binary.
const launcherScript = "launch-commit-composer.sh"

// protectedRefs are the branches a recompose range must not silently rewrite;
// launchMain surfaces a heads-up (SHARED_REF=) when the range overlaps one.
var protectedRefs = []string{"origin/main", "origin/master", "upstream/main", "upstream/master"}

// launchMain is invoked as
//
//	commit-composer __launch [--plugin-root=DIR] [-C DIR] -- <inner args...>
//
// It runs the terminal-overlay launcher (launch-commit-composer.sh) as a child
// process so the slash command only ever issues a single `commit-composer ...`
// command - no `sh -c`, `osascript`, or command substitution of its own. That
// keeps user shell-approval hooks quiet (they only see the clean top-level
// command) and lets one allow-list entry cover the whole plugin.
//
// Two modes, chosen by the first inner arg:
//
//	subcommand mode (inner[0] starts with "__", e.g. __review-proposal): stream
//	    the launcher's stdout straight through (the review outcome JSON) and
//	    mirror its exit code.
//	TUI mode (inner[0] empty or a range like "HEAD~5"): capture the emitted
//	    plan into a temp file and print
//	        PLAN_FILE=<path>
//	        CANCELLED=0|1
//	        SHARED_REF=<protected ref or empty>
func launchMain(args []string) error {
	fs := flag.NewFlagSet("__launch", flag.ContinueOnError)
	pluginRoot := fs.String("plugin-root", "", "plugin root (the dir containing .claude-plugin); usually ${CLAUDE_PLUGIN_ROOT}")
	dir := fs.String("C", "", "run as if started in this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	inner := fs.Args()

	launcher, err := resolveLauncher(*pluginRoot)
	if err != nil {
		return err
	}

	// Subcommand mode: pass through, stream stdout, mirror exit code.
	if len(inner) > 0 && strings.HasPrefix(inner[0], "__") {
		cmd := exec.Command("bash", append([]string{launcher}, inner...)...)
		if *dir != "" {
			cmd.Dir = *dir
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	// TUI mode: the launcher writes the emitted plan to its stdout; capture it
	// into a temp file so large plans never round-trip through the model.
	planFile, err := os.CreateTemp("", "commit-composer-plan-")
	if err != nil {
		return fmt.Errorf("create plan file: %w", err)
	}
	planPath := planFile.Name()

	cmd := exec.Command("bash", append([]string{launcher}, inner...)...)
	if *dir != "" {
		cmd.Dir = *dir
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = planFile
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	planFile.Close()
	if runErr != nil {
		return fmt.Errorf("launch tui: %w", runErr)
	}

	cancelled := true
	if fi, statErr := os.Stat(planPath); statErr == nil && fi.Size() > 0 {
		cancelled = false
	}

	shared := ""
	if !cancelled {
		shared = sharedProtectedRef(context.Background(), git.Repo{Dir: *dir}, planPath)
	}

	fmt.Printf("PLAN_FILE=%s\n", planPath)
	if cancelled {
		fmt.Println("CANCELLED=1")
	} else {
		fmt.Println("CANCELLED=0")
	}
	fmt.Printf("SHARED_REF=%s\n", shared)
	return nil
}

// resolveLauncher finds launch-commit-composer.sh. Preference order:
//
//  1. $COMMIT_COMPOSER_LAUNCHER (explicit override)
//  2. $CLAUDE_PLUGIN_DATA/scripts/<script> (user override; preserves the
//     pre-0.3 resolve-launcher.sh behaviour)
//  3. <plugin-root>/.claude-plugin/scripts/<script> (from --plugin-root)
//  4. $CLAUDE_PLUGIN_ROOT/.claude-plugin/scripts/<script>
//  5. <exe-dir>/../scripts/<script> (dev tree: bin/ sits next to scripts/)
//
// The binary cannot self-locate the launcher under Homebrew (binary in bin/,
// scripts in share/commit-composer/.claude-plugin/), so --plugin-root or the
// CLAUDE_PLUGIN_ROOT env is the primary mechanism there.
func resolveLauncher(pluginRoot string) (string, error) {
	if v := os.Getenv("COMMIT_COMPOSER_LAUNCHER"); v != "" && isExecutable(v) {
		return v, nil
	}
	var candidates []string
	if data := os.Getenv("CLAUDE_PLUGIN_DATA"); data != "" {
		candidates = append(candidates, filepath.Join(data, "scripts", launcherScript))
	}
	if pluginRoot != "" {
		candidates = append(candidates, filepath.Join(pluginRoot, ".claude-plugin", "scripts", launcherScript))
	}
	if pr := os.Getenv("CLAUDE_PLUGIN_ROOT"); pr != "" {
		candidates = append(candidates, filepath.Join(pr, ".claude-plugin", "scripts", launcherScript))
	}
	if exe, err := selfPath(); err == nil {
		candidates = append(candidates, filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "scripts", launcherScript)))
	}
	for _, c := range candidates {
		if isExecutable(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf("cannot locate %s (set COMMIT_COMPOSER_LAUNCHER or pass --plugin-root=${CLAUDE_PLUGIN_ROOT})", launcherScript)
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode()&0o111 != 0
}

// sharedProtectedRef parses the emitted plan and returns the first protected
// ref that already contains any of the plan's (non-working-tree) commits, or
// "" when the range is local-only or the check cannot run. Best-effort: any
// error yields "" so the launch is never blocked on a heads-up.
func sharedProtectedRef(ctx context.Context, repo git.Repo, planPath string) string {
	f, err := os.Open(planPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	p, err := plan.Unmarshal(f)
	if err != nil {
		return ""
	}
	var shas []string
	for _, op := range p.Ops {
		if !git.IsUncommitted(op.SHA) {
			shas = append(shas, op.SHA)
		}
	}
	if len(shas) == 0 {
		return ""
	}
	for _, ref := range protectedRefs {
		contained, err := repo.CommitsContainedIn(ctx, ref, shas)
		if err != nil {
			continue // ref doesn't exist or git errored - skip it
		}
		for _, in := range contained {
			if in {
				return ref
			}
		}
	}
	return ""
}
