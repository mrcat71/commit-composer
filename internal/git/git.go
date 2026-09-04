// Package git wraps the subset of `git` CLI operations commit-composer needs:
// resolving a commit range, reading commit metadata and diffs, and (in
// rebase.go) driving a non-interactive rebase via GIT_SEQUENCE_EDITOR.
//
// All operations shell out to the system `git` binary - go-git is intentionally
// avoided because its rebase support is incomplete.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxDefaultDepth is the safety upper bound for the default range when the
// branch has no upstream-ahead. We don't cap "all commits" hard - the user
// asked for all - but for repos with millions of commits this prevents the
// TUI from blowing up. Practically unlimited for typical user repos.
const maxDefaultDepth = 5000

// minUpstreamCommitsForDefault is the minimum commits-ahead of upstream
// needed before ResolveRange uses upstream..HEAD as the default range.
//
// Below this threshold (e.g. right after pulling a merged PR, when you have
// 0 or 1 local follow-up commits), upstream..HEAD shows a TUI with too
// little context to be useful - so we fall through to the recent-N window
// instead. The user can always pass an explicit range to override.
const minUpstreamCommitsForDefault = 2

// UncommittedSHA is the sentinel "SHA" used for the synthetic virtual row
// that represents staged + unstaged + untracked changes in the working
// tree. It's intentionally not a valid hex SHA so it can't collide with a
// real commit. BuildTodo skips ops with this SHA; Apply handles them via a
// separate stage + commit pass that runs around the rebase.
const UncommittedSHA = "WORKING"

// EmptyTreeSHA is the well-known git hash of an empty tree. Used as a
// synthetic parent for the initial commit so `git diff EMPTY..<sha>` works
// uniformly across normal and root-only commits.
const EmptyTreeSHA = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// IsUncommitted reports whether sha is the synthetic working-tree marker.
func IsUncommitted(sha string) bool { return sha == UncommittedSHA }

// Repo represents a git working tree rooted at Dir.
type Repo struct {
	Dir string // working directory; empty means use the caller's CWD
}

// Run executes `git <args...>` inside r.Dir and returns combined stdout.
// stderr is folded into the returned error on failure.
func (r Repo) Run(ctx context.Context, args ...string) (string, error) {
	return r.runEnv(ctx, nil, args...)
}

// runEnv is Run with extra "K=V" entries layered over the inherited
// environment. Used to point git at a scratch index via GIT_INDEX_FILE.
func (r Repo) runEnv(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	// --no-optional-locks stops read-only commands from taking the index lock,
	// so inspecting a repo never contends with a git the user is running in it.
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks"}, args...)...)
	if r.Dir != "" {
		cmd.Dir = r.Dir
	}
	// LC_ALL=C pins git's own messages to English; every parser in this package
	// and every error-classifying helper (isUnknownRef) assumes that.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.Env = append(cmd.Env, extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// nonEmptyLines splits git output on newlines, dropping blank lines and any
// trailing \r. git emits a trailing newline on almost every command, so a naive
// Split leaves an empty final element.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// RevParse resolves a revision (branch / SHA / HEAD~N / etc) to a full SHA.
func (r Repo) RevParse(ctx context.Context, rev string) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsClean reports whether the working tree has no staged or unstaged changes.
func (r Repo) IsClean(ctx context.Context) (bool, error) {
	out, err := r.Run(ctx, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// commitCount returns the number of commits reachable from ref (all parents).
func (r Repo) commitCount(ctx context.Context, ref string) (int, error) {
	out, err := r.Run(ctx, "rev-list", "--count", ref)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse commit count %q: %w", out, err)
	}
	return n, nil
}

// firstParentDepth returns the first-parent depth of ref - i.e., how many
// HEAD~N rewrites resolve. This is the right metric for choosing HEAD~N as
// a rebase base, because `HEAD~N` walks the first-parent chain, NOT the full
// reachable set. For repos with merge commits, this is less than commitCount.
func (r Repo) firstParentDepth(ctx context.Context, ref string) (int, error) {
	out, err := r.Run(ctx, "rev-list", "--first-parent", "--count", ref)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse first-parent depth %q: %w", out, err)
	}
	return n, nil
}

// UpstreamRange returns the range "<upstream>..HEAD" if the current branch
// tracks an upstream. Returns "" when there is no upstream (e.g. detached HEAD
// or new local branch).
func (r Repo) UpstreamRange(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if err != nil {
		// No upstream is not a hard error - the caller will fall back.
		return "", nil
	}
	upstream := strings.TrimSpace(out)
	if upstream == "" {
		return "", nil
	}
	return upstream + "..HEAD", nil
}

// ResolveRange returns base..head SHAs from a range spec.
//
// Accepted forms:
//
//	"<rev>"           -> "<rev>..HEAD"            (e.g. "HEAD~10")
//	"<base>..<head>"  -> as given (both sides resolved to full SHAs)
//	""                -> upstream..HEAD if it has >= minUpstreamCommitsForDefault
//	                     commits, else HEAD~N..HEAD where N is the first-parent
//	                     depth of HEAD (capped by maxDefaultDepth).
//	                     For a single-commit repo, returns base="" (the
//	                     empty-tree sentinel) + head=HEAD so the initial
//	                     commit can be edited too.
//	                     Errors only if the repo has zero commits.
func (r Repo) ResolveRange(ctx context.Context, spec string) (base, head string, rangeSpec string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		// Try upstream..HEAD if the branch is ahead of upstream by at
		// least minUpstreamCommitsForDefault commits. The threshold avoids
		// the "I just merged a PR and have one tiny follow-up commit"
		// case where upstream..HEAD shows a single-row TUI with no useful
		// context to compare against.
		if up, _ := r.UpstreamRange(ctx); up != "" {
			if cnt, _ := r.commitCount(ctx, up); cnt >= minUpstreamCommitsForDefault {
				spec = up
			}
		}
		// Otherwise fall back to ALL commits reachable along the first-parent
		// chain of HEAD. `HEAD~N` resolves only when N <= first-parent depth
		// (merge commits don't add to that depth even though they're
		// reachable). Using the first-parent depth here avoids "needed a
		// single revision" errors on repos with merge commits.
		if spec == "" {
			n, derr := r.firstParentDepth(ctx, "HEAD")
			if derr != nil {
				// firstParentDepth errors on a repo with zero commits because
				// HEAD does not resolve. Map that to a friendly message rather
				// than leaking git's "ambiguous argument 'HEAD'" stderr.
				if _, perr := r.RevParse(ctx, "HEAD"); perr != nil {
					return "", "", "", fmt.Errorf("repo has no commits yet - make an initial commit first")
				}
				return "", "", "", fmt.Errorf("default range: %w", derr)
			}
			if n == 0 {
				return "", "", "", fmt.Errorf("repo has no commits yet - make an initial commit first")
			}
			if n == 1 {
				// Single commit (the initial commit). Use the empty tree as
				// synthetic base so the user can still recompose / drop /
				// reword it, and so an "uncommitted" row can stack on top.
				head, err = r.RevParse(ctx, "HEAD")
				if err != nil {
					return "", "", "", fmt.Errorf("resolve HEAD: %w", err)
				}
				return "", head, "HEAD", nil
			}
			depth := n - 1
			if depth > maxDefaultDepth {
				depth = maxDefaultDepth
			}
			spec = fmt.Sprintf("HEAD~%d..HEAD", depth)
		}
	}
	var baseRev, headRev string
	if i := strings.Index(spec, ".."); i >= 0 {
		baseRev = spec[:i]
		headRev = spec[i+2:]
		// Reject triple-dot (symmetric difference) - meaningless for rebase.
		if strings.HasPrefix(headRev, ".") {
			return "", "", "", fmt.Errorf("triple-dot range %q is not supported", spec)
		}
		if headRev == "" {
			headRev = "HEAD"
		}
	} else {
		baseRev = spec
		headRev = "HEAD"
	}
	if baseRev == "" {
		return "", "", "", fmt.Errorf("empty base in range %q", spec)
	}
	base, err = r.RevParse(ctx, baseRev)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve base %q: %w", baseRev, err)
	}
	head, err = r.RevParse(ctx, headRev)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve head %q: %w", headRev, err)
	}
	return base, head, baseRev + ".." + headRev, nil
}

// Log returns the commits in base..head, ordered oldest-first (so the slice
// reads top-to-bottom the same way `git rebase -i` shows them).
//
// When base is empty (single-commit repo), runs `git log head` without a
// range so the root commit is included.
func (r Repo) Log(ctx context.Context, base, head string) ([]Commit, error) {
	const sep = "\x1f"
	const recsep = "\x1e"
	format := strings.Join([]string{"%H", "%h", "%an", "%ae", "%aI", "%s", "%b"}, sep)
	args := []string{"log", "--reverse", "--no-merges", "--format=" + format + recsep}
	if base == "" {
		args = append(args, head)
	} else {
		args = append(args, base+".."+head)
	}
	out, err := r.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	records := strings.Split(out, recsep+"\n")
	var commits []Commit
	for _, rec := range records {
		rec = strings.TrimSuffix(rec, recsep)
		rec = strings.TrimRight(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, sep, 7)
		if len(parts) < 7 {
			return nil, fmt.Errorf("malformed log record (%d fields): %q", len(parts), rec)
		}
		date, _ := time.Parse(time.RFC3339, parts[4])
		commits = append(commits, Commit{
			SHA:     parts[0],
			Short:   parts[1],
			Author:  parts[2],
			Email:   parts[3],
			Date:    date,
			Subject: parts[5],
			Body:    strings.TrimRight(parts[6], "\n"),
		})
	}
	return commits, nil
}

// Subjects maps each given SHA to its commit subject in one `git log` call.
//
// Best-effort: SHAs git could not resolve are simply absent from the result, and
// a total failure returns an empty map rather than an error. Both call sites use
// subjects decoratively (todo-file comments, pool listings), so a missing one
// must never fail the operation.
func (r Repo) Subjects(ctx context.Context, shas []string) map[string]string {
	subjects := make(map[string]string, len(shas))
	if len(shas) == 0 {
		return subjects
	}
	// --no-walk restricts output to exactly the named commits instead of
	// traversing their history. Trailing "--" keeps a SHA that also names a file
	// from being read as a pathspec.
	args := append([]string{"log", "--no-walk", "--format=%H %s"}, shas...)
	args = append(args, "--")
	out, err := r.Run(ctx, args...)
	if err != nil {
		// One unresolvable SHA fails the whole batch, so fall back to per-SHA
		// lookups to salvage the rest. Only reachable if the plan references a
		// commit that has since disappeared.
		for _, sha := range shas {
			if one, err := r.Run(ctx, "log", "-1", "--format=%s", sha); err == nil {
				subjects[sha] = strings.TrimSpace(one)
			}
		}
		return subjects
	}
	for _, line := range nonEmptyLines(out) {
		sha, subject, ok := strings.Cut(line, " ")
		if !ok {
			// A commit with an empty subject prints just the SHA.
			sha, subject = line, ""
		}
		subjects[sha] = subject
	}
	return subjects
}

// Files returns the name-status entries touched by a commit.
//
// Uses `git diff-tree --root` so the root commit reports its files as added
// rather than coming back empty.
func (r Repo) Files(ctx context.Context, sha string) ([]FileStat, error) {
	out, err := r.Run(ctx, "diff-tree", "--no-commit-id", "--name-status", "-r", "--root", sha)
	if err != nil {
		return nil, err
	}
	var files []FileStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// Status is a single letter (M/A/D/T/U) or letter+score (R100, C75).
		// Split on first run of whitespace; path may contain spaces (kept as-is).
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			continue
		}
		status := line[:i]
		path := strings.TrimLeft(line[i+1:], " \t")
		files = append(files, FileStat{Status: status, Path: path})
	}
	return files, nil
}

// maxPathsPerCall caps how many pathspecs go into one `git add`, so a repo with
// a huge untracked set can't overflow the OS argument limit.
const maxPathsPerCall = 512

// scratchIndex copies the repo's index to a temp file and returns its path plus
// a cleanup func. Staging into the copy via GIT_INDEX_FILE lets a read-only
// operation use `git add` without ever touching the index the user is working
// in - so an interrupted run can't leave entries behind.
func (r Repo) scratchIndex(ctx context.Context) (string, func(), error) {
	out, err := r.Run(ctx, "rev-parse", "--git-path", "index")
	if err != nil {
		return "", nil, fmt.Errorf("locate index: %w", err)
	}
	realIndex := strings.TrimSpace(out)
	if r.Dir != "" && !filepath.IsAbs(realIndex) {
		realIndex = filepath.Join(r.Dir, realIndex)
	}

	tmp, err := os.CreateTemp("", "commit-composer-index")
	if err != nil {
		return "", nil, fmt.Errorf("scratch index: %w", err)
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }

	src, err := os.Open(realIndex)
	if err != nil {
		// A repo with no commits and nothing staged has no index file yet.
		// Leaving the scratch file empty is fine - git writes a fresh index
		// into it, which is exactly the empty state we'd have copied.
		_ = tmp.Close()
		if os.IsNotExist(err) {
			return tmp.Name(), cleanup, nil
		}
		cleanup()
		return "", nil, fmt.Errorf("read index %s: %w", realIndex, err)
	}
	_, copyErr := io.Copy(tmp, src)
	_ = src.Close()
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("copy index: %w", copyErr)
	}
	return tmp.Name(), cleanup, nil
}

// readableUntracked drops paths git would fail to hash. `git add -N` accepts an
// unreadable file (intent-to-add never reads the content), but the following
// `git diff` has to hash the working-tree blob and aborts the entire diff with
// "fatal: cannot hash <path>" - losing every other file's changes along with it.
// Filtering here costs one stat/open per untracked path and no subprocesses.
func (r Repo) readableUntracked(paths []string) []string {
	keep := make([]string, 0, len(paths))
	for _, p := range paths {
		full := p
		if r.Dir != "" {
			full = filepath.Join(r.Dir, p)
		}
		// git hashes a symlink's target string rather than reading through it,
		// so a symlink is always fine - and opening one would follow it and
		// wrongly reject a dangling link.
		if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			keep = append(keep, p)
			continue
		}
		f, err := os.Open(full)
		if err != nil {
			continue
		}
		_ = f.Close()
		keep = append(keep, p)
	}
	return keep
}

// UncommittedDiff returns the combined diff of staged + unstaged + untracked
// changes vs HEAD as a single patch.
//
// Untracked files only show up in `git diff` once they have an index entry, so
// they are staged with `git add -N` (intent-to-add) against a throwaway copy of
// the index. The user's real index is never written to.
//
// The returned patch can be fed to Claude the same way a commit diff is.
func (r Repo) UncommittedDiff(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", fmt.Errorf("list untracked: %w", err)
	}
	untracked := r.readableUntracked(nonEmptyLines(out))
	if len(untracked) == 0 {
		diff, err := r.Run(ctx, "diff", "--no-color", "HEAD")
		if err != nil {
			return "", fmt.Errorf("git diff HEAD: %w", err)
		}
		return diff, nil
	}

	index, cleanup, err := r.scratchIndex(ctx)
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + index}

	for chunk := range slices.Chunk(untracked, maxPathsPerCall) {
		args := append([]string{"add", "-N", "--"}, chunk...)
		if _, err := r.runEnv(ctx, env, args...); err == nil {
			continue
		}
		// One bad path (vanished mid-run, unreadable) fails the whole chunk.
		// Retry individually so the rest still make it into the diff.
		for _, p := range chunk {
			_, _ = r.runEnv(ctx, env, "add", "-N", "--", p)
		}
	}

	diff, err := r.runEnv(ctx, env, "diff", "--no-color", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff HEAD: %w", err)
	}
	return diff, nil
}

// UncommittedFiles returns the name-status entries for every file in the
// working tree that differs from HEAD: staged, unstaged, or untracked.
func (r Repo) UncommittedFiles(ctx context.Context) ([]FileStat, error) {
	// Tracked changes (staged + unstaged) from HEAD.
	out, err := r.Run(ctx, "diff", "--name-status", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("diff name-status HEAD: %w", err)
	}
	var files []FileStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		i := strings.IndexAny(line, " \t")
		if i < 0 {
			continue
		}
		files = append(files, FileStat{Status: line[:i], Path: strings.TrimLeft(line[i+1:], " \t")})
	}
	// Untracked: treat as additions.
	untracked, err := r.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, fmt.Errorf("list untracked: %w", err)
	}
	for _, p := range strings.Split(strings.TrimRight(untracked, "\n"), "\n") {
		if p == "" {
			continue
		}
		files = append(files, FileStat{Status: "A", Path: p})
	}
	return files, nil
}

// StashPushIncludeUntracked stashes everything (staged + unstaged +
// untracked) into a single entry tagged with the supplied message. Returns
// true if a stash was actually created (false when the tree was already
// clean).
func (r Repo) StashPushIncludeUntracked(ctx context.Context, msg string) (bool, error) {
	clean, err := r.IsClean(ctx)
	if err != nil {
		return false, err
	}
	if clean {
		// Even with --include-untracked we want to skip if there's truly
		// nothing to stash, to avoid empty-stash noise.
		out, _ := r.Run(ctx, "ls-files", "--others", "--exclude-standard")
		if strings.TrimSpace(out) == "" {
			return false, nil
		}
	}
	if _, err := r.Run(ctx, "stash", "push", "--include-untracked", "-m", msg); err != nil {
		return false, fmt.Errorf("git stash push: %w", err)
	}
	return true, nil
}

// StashPop pops the most-recent stash entry. Returns the merge-conflict
// error verbatim so the caller can surface it to the user.
func (r Repo) StashPop(ctx context.Context) error {
	_, err := r.Run(ctx, "stash", "pop")
	return err
}

// Diff returns the patch produced by `git show <sha>` (no metadata header,
// just the unified diff body).
func (r Repo) Diff(ctx context.Context, sha string) (string, error) {
	out, err := r.Run(ctx, "show", "--no-color", "--patch", "--format=", sha)
	if err != nil {
		return "", err
	}
	return strings.TrimLeft(out, "\n"), nil
}

// DiffPaths returns the patch for the given paths between base and head.
// When base is empty, diffs against the empty tree (root-commit case). When
// paths is empty, returns the full diff for the whole tree.
func (r Repo) DiffPaths(ctx context.Context, base, head string, paths []string) (string, error) {
	if base == "" {
		base = EmptyTreeSHA
	}
	args := []string{"diff", "--no-color", base, head}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := r.Run(ctx, args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// ParentOrEmpty returns the parent commit of sha walked back n steps along
// the first-parent chain. If n reaches the root, returns EmptyTreeSHA (the
// well-known empty-tree hash) so diffs work uniformly.
func (r Repo) ParentOrEmpty(ctx context.Context, sha string, n int) (string, error) {
	if n < 1 {
		n = 1
	}
	out, err := r.Run(ctx, "rev-parse", "--verify", fmt.Sprintf("%s~%d^{commit}", sha, n))
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	// rev-parse failed -> we walked past the root. Use empty tree.
	return EmptyTreeSHA, nil
}

// CommitsContainedIn reports which of the given SHAs are reachable from ref.
// Used by the slash command's safety check to refuse rebasing commits that are
// already published to a protected branch.
//
// Returns an empty map (not an error) when ref does not exist, e.g. a repo with
// no origin/main: nothing is published, so nothing needs protecting.
func (r Repo) CommitsContainedIn(ctx context.Context, ref string, shas []string) (map[string]bool, error) {
	contained := make(map[string]bool, len(shas))
	if len(shas) == 0 {
		return contained, nil
	}
	// One rev-list instead of a merge-base per SHA. Anything it prints is NOT
	// reachable from ref; every input absent from the output is. The output can
	// include ancestors of the non-contained SHAs too, so only look up the SHAs
	// we asked about.
	args := append([]string{"rev-list"}, shas...)
	args = append(args, "--not", ref)
	out, err := r.Run(ctx, args...)
	if err != nil {
		// git reports a missing ref on stderr, which Run folds into the error.
		if isUnknownRef(err.Error()) {
			return contained, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Some other rev-list failure. Report nothing as contained: the
			// safety check will let the rebase proceed rather than block on an
			// answer we don't have.
			return contained, nil
		}
		return nil, fmt.Errorf("rev-list %s: %w", ref, err)
	}
	notContained := make(map[string]bool, len(shas))
	for _, sha := range nonEmptyLines(out) {
		notContained[sha] = true
	}
	for _, sha := range shas {
		contained[sha] = !notContained[sha]
	}
	return contained, nil
}

func isUnknownRef(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "unknown revision") || strings.Contains(s, "not a valid object name")
}

// FormatRebaseCommand returns a copy-pastable `git rebase -i <base>` command
// for the user to inspect.
func FormatRebaseCommand(base string) string {
	return "git rebase -i " + base
}

// formatHumanCount renders a small integer for status lines ("3 commits").
func formatHumanCount(n int, singular, plural string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + singular
	}
	return strconv.Itoa(n) + " " + plural
}
