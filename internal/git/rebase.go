package git

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mrcat71/commit-composer/internal/plan"
)

// SplitGroup is one of N resulting commits in a pre-approved recompose.
//
// Files is the legacy file-level scope; every diff hunk touching one of the
// listed paths joins this commit.
//
// Hunks is the line-level scope; values are indices into the pool's parsed
// diff (the array written to <sha>.hunks.json by __split-prepare). When
// Hunks is non-empty it takes precedence over Files: only the indexed hunks
// land in this commit. Mixing the two is not supported.
type SplitGroup struct {
	Files   []string `json:"files"`
	Hunks   []int    `json:"hunks,omitempty"`
	Message string   `json:"message"`
}

// SplitSpec is the JSON the slash command writes for each claude-recompose
// POOL (one or more consecutive marked commits).
//
// Stored at <splits-dir>/<sha>.split.json where <sha> is the SHA of the LAST
// commit in the pool (the one the todo marks `edit`). PoolSize >= 1 tells
// executeSplit how far to reset before applying the new groups.
type SplitSpec struct {
	SHA      string       `json:"sha"`
	PoolSize int          `json:"pool_size,omitempty"` // default 1
	Groups   []SplitGroup `json:"groups"`
}

// BuildTodo renders a Plan to the `git rebase -i` todo-file format.
//
// For ClaudeRecompose pools (runs of consecutive marked commits), only the
// LAST commit of each pool is emitted as `edit` - the earlier commits in the
// pool stay `pick` so they apply normally. When the rebase pauses on the
// last commit, the apply phase has all of the pool's changes in the tree and
// can `git reset HEAD~<poolSize>` before applying the pre-approved groups.
func BuildTodo(p plan.Plan, subjects map[string]string) string {
	poolEnd := recomposePoolEnds(p.Ops)
	var b strings.Builder
	for i, op := range p.Ops {
		if IsUncommitted(op.SHA) {
			// The synthetic uncommitted row is handled outside the rebase
			// (Apply commits its groups on top of HEAD afterwards).
			continue
		}
		verb := todoVerb(op.Action, i, poolEnd)
		subj := subjects[op.SHA]
		fmt.Fprintf(&b, "%s %s %s\n", verb, op.SHA, subj)
	}
	return b.String()
}

func todoVerb(a plan.Action, i int, poolEnd map[int]bool) string {
	switch a {
	case plan.Pick:
		return "pick"
	case plan.Reword:
		return "reword"
	case plan.Squash:
		return "squash"
	case plan.Fixup:
		return "fixup"
	case plan.Drop:
		return "drop"
	case plan.Edit:
		return "edit"
	case plan.ClaudeRecompose:
		if poolEnd[i] {
			return "edit" // pause to apply the proposed groups
		}
		return "pick" // intermediate pool commits apply normally
	}
	return "pick"
}

// recomposePoolEnds returns a map of indices that mark the last commit of a
// run of consecutive ClaudeRecompose ops.
func recomposePoolEnds(ops []plan.Op) map[int]bool {
	out := make(map[int]bool)
	for i := 0; i < len(ops); i++ {
		if ops[i].Action != plan.ClaudeRecompose {
			continue
		}
		j := i
		for j+1 < len(ops) && ops[j+1].Action == plan.ClaudeRecompose {
			j++
		}
		out[j] = true
		i = j
	}
	return out
}

// TrimLeadingPicks shortens the rebase scope by advancing Base past any
// leading run of pure pass-through pick ops (Action == Pick, OrigIndex == i,
// no reword/granularity/etc). Those commits don't need to be re-applied -
// they'd produce identical SHAs anyway - so skipping them avoids replaying
// hundreds of unrelated commits and the conflicts they may trigger.
//
// Returns the trimmed plan. If nothing can be trimmed, returns p unchanged.
func TrimLeadingPicks(p plan.Plan) plan.Plan {
	k := 0
	for k < len(p.Ops) {
		op := p.Ops[k]
		if op.Action != plan.Pick {
			break
		}
		if op.OrigIndex != k {
			// Reordered - all bets off, can't trim.
			break
		}
		if op.NewMessage != "" {
			break
		}
		k++
	}
	if k == 0 {
		return p
	}
	return plan.Plan{
		Base:  p.Ops[k-1].SHA,
		Range: p.Range,
		Ops:   p.Ops[k:],
	}
}

// RecomposePools returns the SHA list of each pool, keyed by the last
// commit's SHA. Used by the slash command to pre-analyse pools before apply.
//
// Each entry's value is the ordered list of commit SHAs in that pool
// (oldest first).
func RecomposePools(p plan.Plan) map[string][]string {
	out := make(map[string][]string)
	i := 0
	for i < len(p.Ops) {
		if p.Ops[i].Action != plan.ClaudeRecompose {
			i++
			continue
		}
		j := i
		for j+1 < len(p.Ops) && p.Ops[j+1].Action == plan.ClaudeRecompose {
			j++
		}
		shas := make([]string, 0, j-i+1)
		for k := i; k <= j; k++ {
			shas = append(shas, p.Ops[k].SHA)
		}
		out[p.Ops[j].SHA] = shas
		i = j + 1
	}
	return out
}

// RewordMessages extracts the per-SHA new messages for reword ops.
func RewordMessages(p plan.Plan) map[string]string {
	out := make(map[string]string)
	for _, op := range p.Ops {
		if op.Action == plan.Reword && op.NewMessage != "" {
			out[op.SHA] = op.NewMessage
		}
	}
	return out
}

// ApplyOptions configures the rebase execution.
type ApplyOptions struct {
	// SelfExe is the absolute path to the commit-composer binary; used as the
	// helper for GIT_SEQUENCE_EDITOR and GIT_EDITOR.
	SelfExe string

	// SplitsDir is the directory where the slash command wrote
	// <sha>.split.json files for pre-approved claude-split commits. May be
	// empty if the plan has no claude-split ops. Required if the plan
	// contains any ClaudeSplit action.
	SplitsDir string

	// Stdout / Stderr receive git's output. The caller is responsible for
	// displaying them to the user.
	Stdout io.Writer
	Stderr io.Writer
}

// Apply runs `git rebase -i <base>` driven by GIT_SEQUENCE_EDITOR / GIT_EDITOR
// helpers that read the pre-generated todo and reword messages from a temp
// directory.
//
// On success, the rebase has completed. On rebase conflict, the function
// returns a non-nil error and the working tree is left in the conflicted
// state - the caller must surface this to the user (they run `git rebase
// --continue` or `git rebase --abort` themselves).
//
// If the plan contains an "uncommitted" op (SHA == UncommittedSHA), Apply:
//  1. stashes the working tree (so the rebase can run cleanly)
//  2. runs the rebase for any other ops
//  3. pops the stash back
//  4. commits the uncommitted groups (from <UncommittedSHA>.split.json) on
//     top of the new HEAD
func (r Repo) Apply(ctx context.Context, p plan.Plan, opts ApplyOptions) error {
	if len(p.Ops) == 0 {
		return errors.New("plan has no ops")
	}
	// Empty base is the single-commit / root-commit case; rebase uses --root.
	if opts.SelfExe == "" {
		return errors.New("ApplyOptions.SelfExe is required")
	}

	// Pull the uncommitted op (if any) out of the regular rebase ops; it's
	// applied as a separate commit-on-top step after the rebase.
	var uncommitted *plan.Op
	rebaseOps := make([]plan.Op, 0, len(p.Ops))
	for i := range p.Ops {
		if IsUncommitted(p.Ops[i].SHA) {
			op := p.Ops[i]
			uncommitted = &op
			continue
		}
		rebaseOps = append(rebaseOps, p.Ops[i])
	}
	p.Ops = rebaseOps

	// Collect subjects for the todo file's trailing comment column.
	subjects := make(map[string]string, len(p.Ops))
	for _, op := range p.Ops {
		// Best-effort: read the subject. Failure is non-fatal; the comment
		// column is decorative.
		out, err := r.Run(ctx, "log", "-1", "--format=%s", op.SHA)
		if err == nil {
			subjects[op.SHA] = strings.TrimSpace(out)
		}
	}

	stage, err := os.MkdirTemp("", "commit-composer-rebase-")
	if err != nil {
		return fmt.Errorf("mkdir stage: %w", err)
	}
	// Caller is responsible for cleanup post-rebase; we keep the stage
	// directory around so `git rebase --continue` can still find reword
	// messages mid-rebase. The path is logged via stderr.
	todoPath := filepath.Join(stage, "todo")
	if err := os.WriteFile(todoPath, []byte(BuildTodo(p, subjects)), 0o600); err != nil {
		return fmt.Errorf("write todo: %w", err)
	}

	// Persist reword messages by SHA so the GIT_EDITOR helper can look them
	// up. File names are <sha>.msg (full 40-char SHA).
	for sha, msg := range RewordMessages(p) {
		path := filepath.Join(stage, sha+".msg")
		if err := os.WriteFile(path, []byte(msg+"\n"), 0o600); err != nil {
			return fmt.Errorf("write reword msg %s: %w", sha, err)
		}
	}

	env := append(os.Environ(),
		"COMMIT_COMPOSER_STAGE="+stage,
		"GIT_SEQUENCE_EDITOR="+opts.SelfExe+" __sequence-editor",
		"GIT_EDITOR="+opts.SelfExe+" __commit-editor",
	)

	// Up-front check: every claude-split sha must have a JSON in SplitsDir.
	if err := validateSplits(p, opts.SplitsDir); err != nil {
		return err
	}

	// If there's an uncommitted op marked recompose, validate its JSON now
	// too so we fail fast before any state mutation.
	if uncommitted != nil && uncommitted.Action == plan.ClaudeRecompose {
		spec, ok := loadSplitSpec(opts.SplitsDir, UncommittedSHA)
		if !ok {
			return fmt.Errorf("missing %s.split.json for uncommitted recompose (looked in %s)", UncommittedSHA, opts.SplitsDir)
		}
		if len(spec.Groups) < 1 {
			return fmt.Errorf("uncommitted recompose needs at least 1 group")
		}
		for gi, g := range spec.Groups {
			if len(g.Files) == 0 && len(g.Hunks) == 0 {
				return fmt.Errorf("uncommitted group %d has neither files nor hunks", gi)
			}
			if strings.TrimSpace(g.Message) == "" {
				return fmt.Errorf("uncommitted group %d has empty message", gi)
			}
		}
	}

	// Stash the working tree if there are rebase ops AND we have either
	// an uncommitted op or just a dirty tree we want to preserve. Skipping
	// when there are no rebase ops is OK - we'll just commit the WT groups
	// directly without needing the rebase to run.
	stashed := false
	if uncommitted != nil && len(p.Ops) > 0 {
		var err error
		stashed, err = r.StashPushIncludeUntracked(ctx, "commit-composer apply: stash before rebase")
		if err != nil {
			return fmt.Errorf("stash before rebase: %w", err)
		}
	}

	runGit := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", args...)
		if r.Dir != "" {
			cmd.Dir = r.Dir
		}
		cmd.Env = env
		cmd.Stdout = opts.Stdout
		cmd.Stderr = opts.Stderr
		return cmd.Run()
	}

	// If there are no rebase ops (only uncommitted), skip the rebase entirely.
	if len(p.Ops) > 0 {
		rebaseArgs := []string{"rebase", "-i"}
		baseLabel := p.Base
		if p.Base == "" {
			rebaseArgs = append(rebaseArgs, "--root")
			baseLabel = "--root"
		} else {
			rebaseArgs = append(rebaseArgs, p.Base)
		}
		if err := runGit(rebaseArgs...); err != nil {
			return fmt.Errorf("git rebase -i %s: %w (stage=%s)", baseLabel, err, stage)
		}
	}

	// Loop: git rebase exits 0 even when paused on an edit step. We check
	// whether a rebase is still in progress and, if it is, handle the stop
	// point.
	for {
		inProgress, err := r.RebaseInProgress(ctx)
		if err != nil {
			return fmt.Errorf("check rebase state: %w", err)
		}
		if !inProgress {
			break
		}

		gitDir, err := r.gitDir(ctx)
		if err != nil {
			return fmt.Errorf("locate git dir: %w", err)
		}
		verb, sha := lastDoneStep(filepath.Join(gitDir, "rebase-merge", "done"))
		if verb == "" {
			return fmt.Errorf("rebase paused but could not identify the step from %s", filepath.Join(gitDir, "rebase-merge", "done"))
		}

		spec, ok := loadSplitSpec(opts.SplitsDir, sha)
		if !ok {
			// Plain edit pause - hand control back to the user.
			return fmt.Errorf("rebase paused for manual edit on %s - resolve and run 'git rebase --continue' (stage=%s)", short(sha), stage)
		}

		if err := r.executeSplit(ctx, spec); err != nil {
			return fmt.Errorf("execute split for %s: %w (stage=%s)", short(sha), err, stage)
		}
		if err := runGit("rebase", "--continue"); err != nil {
			return fmt.Errorf("git rebase --continue after split %s: %w (stage=%s)", short(sha), err, stage)
		}
	}

	// Rebase done. Now pop the stash (if we stashed) and apply the
	// uncommitted recompose groups, if any.
	if stashed {
		if err := r.StashPop(ctx); err != nil {
			return fmt.Errorf("git stash pop after rebase: %w (your rebased history is in place; the original uncommitted changes are still in stash@{0})", err)
		}
	}
	if uncommitted != nil && uncommitted.Action == plan.ClaudeRecompose {
		spec, _ := loadSplitSpec(opts.SplitsDir, UncommittedSHA)
		if err := r.executeUncommittedRecompose(ctx, spec); err != nil {
			return fmt.Errorf("commit uncommitted groups: %w", err)
		}
	}

	_ = os.RemoveAll(stage)
	return nil
}

// executeUncommittedRecompose stages and commits each group from a working-
// tree SplitSpec. Unlike executeSplit (which operates after a rebase reset),
// this just commits the existing working tree contents - no reset, no
// HEAD~N math.
func (r Repo) executeUncommittedRecompose(ctx context.Context, spec SplitSpec) error {
	// Unstage everything first so we control which files land in each commit.
	if _, err := r.Run(ctx, "reset"); err != nil {
		return fmt.Errorf("git reset (clear index): %w", err)
	}
	for i, g := range spec.Groups {
		args := append([]string{"add", "--"}, g.Files...)
		if _, err := r.Run(ctx, args...); err != nil {
			return fmt.Errorf("git add group %d: %w", i, err)
		}
		if _, err := r.Run(ctx, "commit", "-m", g.Message); err != nil {
			return fmt.Errorf("git commit group %d: %w", i, err)
		}
	}
	clean, err := r.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		out, _ := r.Run(ctx, "status", "--porcelain")
		return fmt.Errorf("uncommitted recompose: working tree not clean after groups - some files were not covered:\n%s",
			strings.TrimSpace(out))
	}
	return nil
}

// validateSplits ensures every ClaudeRecompose pool has a matching JSON in
// splitsDir, keyed by the LAST commit of each pool. Returns an error before
// the rebase starts if anything is missing or malformed.
func validateSplits(p plan.Plan, splitsDir string) error {
	poolEnd := recomposePoolEnds(p.Ops)
	for i, op := range p.Ops {
		if op.Action != plan.ClaudeRecompose || !poolEnd[i] {
			continue
		}
		if splitsDir == "" {
			return fmt.Errorf("plan has claude-recompose pool ending at %s but no --splits directory was provided", short(op.SHA))
		}
		spec, ok := loadSplitSpec(splitsDir, op.SHA)
		if !ok {
			return fmt.Errorf("missing recompose JSON for %s in %s", short(op.SHA), splitsDir)
		}
		if len(spec.Groups) < 1 {
			return fmt.Errorf("recompose %s needs at least 1 group", short(op.SHA))
		}
		// Single-group "recompose" is fine - it's essentially a reword + maybe
		// file reorganisation - but warn? Skip the warning, just allow it.
		for gi, g := range spec.Groups {
			if len(g.Files) == 0 && len(g.Hunks) == 0 {
				return fmt.Errorf("recompose %s group %d has neither files nor hunks", short(op.SHA), gi)
			}
			if strings.TrimSpace(g.Message) == "" {
				return fmt.Errorf("recompose %s group %d has empty message", short(op.SHA), gi)
			}
		}
		if spec.PoolSize == 0 {
			// Backward-compat with old single-commit splits.
			spec.PoolSize = 1
		}
	}
	return nil
}

// executeSplit applies a SplitSpec: reset HEAD by PoolSize commits, then for
// each group commit the listed files (file-level) or only the indexed hunks
// (line-level), depending on which the group specifies.
//
// Asserts the working tree is clean afterward (every changed line must
// belong to some group).
func (r Repo) executeSplit(ctx context.Context, spec SplitSpec) error {
	n := spec.PoolSize
	if n < 1 {
		n = 1
	}
	if _, err := r.Run(ctx, "reset", "--mixed", fmt.Sprintf("HEAD~%d", n)); err != nil {
		return fmt.Errorf("git reset HEAD~%d: %w", n, err)
	}

	// If any group uses hunk-level scope, parse the full pool diff up front
	// so we can slice it per group. The diff is taken against HEAD (which is
	// now the pool's parent after the reset above), giving the same content
	// the prep step indexed.
	var poolHunks []Hunk
	wantsHunks := false
	for _, g := range spec.Groups {
		if len(g.Hunks) > 0 {
			wantsHunks = true
			break
		}
	}
	if wantsHunks {
		full, err := r.Run(ctx, "diff", "--no-color", "HEAD")
		if err != nil {
			return fmt.Errorf("recompute pool diff for hunk apply: %w", err)
		}
		poolHunks, err = ParseHunks(full)
		if err != nil {
			return fmt.Errorf("parse pool diff: %w", err)
		}
	}

	for i, g := range spec.Groups {
		if len(g.Hunks) > 0 {
			if err := r.applyHunkGroup(ctx, i, g, poolHunks); err != nil {
				return err
			}
		} else {
			args := append([]string{"add", "--"}, g.Files...)
			if _, err := r.Run(ctx, args...); err != nil {
				return fmt.Errorf("git add group %d: %w", i, err)
			}
		}
		if _, err := r.Run(ctx, "commit", "-m", g.Message); err != nil {
			return fmt.Errorf("git commit group %d: %w", i, err)
		}
	}
	clean, err := r.IsClean(ctx)
	if err != nil {
		return err
	}
	if !clean {
		out, _ := r.Run(ctx, "status", "--porcelain")
		return fmt.Errorf("recompose %s: working tree not clean after groups - some lines were not covered:\n%s",
			short(spec.SHA), strings.TrimSpace(out))
	}
	return nil
}

// applyHunkGroup stages only the indexed hunks for one group and leaves the
// rest in the working tree for subsequent groups to claim. Uses
// `git apply --cached --recount` so small line-offset shifts from earlier
// groups do not break later applies.
func (r Repo) applyHunkGroup(ctx context.Context, gi int, g SplitGroup, all []Hunk) error {
	picked := make([]Hunk, 0, len(g.Hunks))
	for _, idx := range g.Hunks {
		if idx < 0 || idx >= len(all) {
			return fmt.Errorf("group %d hunk index %d out of range (pool has %d hunks)", gi, idx, len(all))
		}
		picked = append(picked, all[idx])
	}
	patch := BuildPatch(picked)
	if patch == "" {
		return fmt.Errorf("group %d: empty patch from %d hunk(s)", gi, len(g.Hunks))
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--cached", "--recount", "-")
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	cmd.Stdin = strings.NewReader(patch)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git apply --cached group %d: %w: %s", gi, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// RebaseInProgress reports whether the repo is currently in an interrupted
// rebase (rebase-merge/ or rebase-apply/ exists in the git dir).
func (r Repo) RebaseInProgress(ctx context.Context) (bool, error) {
	gitDir, err := r.gitDir(ctx)
	if err != nil {
		return false, err
	}
	for _, sub := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, sub)); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func (r Repo) gitDir(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	gd := strings.TrimSpace(out)
	if !filepath.IsAbs(gd) {
		base := r.Dir
		if base == "" {
			base, _ = os.Getwd()
		}
		gd = filepath.Join(base, gd)
	}
	return gd, nil
}

// lastDoneStep parses the .git/rebase-merge/done file and returns the verb
// and SHA of the most recent processed step.
func lastDoneStep(donePath string) (verb, sha string) {
	data, err := os.ReadFile(donePath)
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			return fields[0], fields[1]
		}
	}
	return "", ""
}

func loadSplitSpec(dir, sha string) (SplitSpec, bool) {
	if dir == "" || sha == "" {
		return SplitSpec{}, false
	}
	path := filepath.Join(dir, sha+".split.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return SplitSpec{}, false
	}
	var s SplitSpec
	if err := json.Unmarshal(data, &s); err != nil {
		return SplitSpec{}, false
	}
	return s, true
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// SequenceEditorMain is invoked when commit-composer is re-entered as
// GIT_SEQUENCE_EDITOR. argv[1] is the path to git's generated todo file; we
// overwrite it with the user's pre-built todo from COMMIT_COMPOSER_STAGE.
func SequenceEditorMain(args []string) error {
	if len(args) < 1 {
		return errors.New("sequence-editor: no todo path argument")
	}
	stage := os.Getenv("COMMIT_COMPOSER_STAGE")
	if stage == "" {
		return errors.New("sequence-editor: COMMIT_COMPOSER_STAGE env not set")
	}
	src := filepath.Join(stage, "todo")
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("sequence-editor: read staged todo: %w", err)
	}
	if err := os.WriteFile(args[0], data, 0o600); err != nil {
		return fmt.Errorf("sequence-editor: write todo: %w", err)
	}
	return nil
}

// CommitEditorMain is invoked as GIT_EDITOR during a reword. It is called
// with the path to .git/COMMIT_EDITMSG. If the rebase is currently processing
// a `reword` entry from our staged todo, we replace the file with the staged
// new message.
//
// Strategy: read `.git/rebase-merge/done`, find the last line, parse out the
// SHA. The line format is `reword <sha> <subject>`. We use the SHA to look up
// our staged `<sha>.msg` file.
func CommitEditorMain(args []string) error {
	if len(args) < 1 {
		return errors.New("commit-editor: no message path argument")
	}
	stage := os.Getenv("COMMIT_COMPOSER_STAGE")
	if stage == "" {
		// Not driven by us - leave message alone.
		return nil
	}
	msgPath := args[0]
	cur, err := os.ReadFile(msgPath)
	if err != nil {
		return fmt.Errorf("commit-editor: read msg: %w", err)
	}

	gitDir := findGitDir(msgPath)
	sha := lastRewordSHA(filepath.Join(gitDir, "rebase-merge", "done"))
	if sha == "" {
		return nil
	}
	stagedPath := filepath.Join(stage, sha+".msg")
	data, err := os.ReadFile(stagedPath)
	if err != nil {
		return nil // no staged message for this SHA - leave original alone
	}
	merged := bytes.TrimRight(data, "\n")
	merged = append(merged, '\n')
	merged = append(merged, extractCommentBlock(cur)...)
	return os.WriteFile(msgPath, merged, 0o600)
}

// findGitDir returns the .git directory that contains the given commit
// message path. Handles both standard and worktree layouts.
//
// COMMIT_EDITMSG typically lives at <gitdir>/COMMIT_EDITMSG, so the parent of
// the file is the git dir.
func findGitDir(msgPath string) string {
	return filepath.Dir(msgPath)
}

// lastRewordSHA reads the rebase `done` file and returns the SHA of the most
// recently processed `reword` line. Empty string if there is no reword line
// or the file is missing.
func lastRewordSHA(donePath string) string {
	data, err := os.ReadFile(donePath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Fields(lines[i])
		if len(fields) >= 2 && (fields[0] == "reword" || fields[0] == "r") {
			return fields[1]
		}
	}
	return ""
}

func extractCommentBlock(b []byte) []byte {
	// Return a trailing block of lines starting with '#' so git's
	// scissors-line + status hints survive.
	lines := bytes.Split(b, []byte("\n"))
	startComment := -1
	for i, l := range lines {
		if len(l) > 0 && l[0] == '#' {
			if startComment < 0 {
				startComment = i
			}
		}
	}
	if startComment < 0 {
		return nil
	}
	return append([]byte{'\n'}, bytes.Join(lines[startComment:], []byte("\n"))...)
}
