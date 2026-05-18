// commit-composer is a TUI for marking commits with rebase actions
// (pick / reword / squash / fixup / drop / edit) and (optionally) applying
// them via `git rebase -i`.
//
// Modes:
//
//	commit-composer [range]              # interactive TUI, prints plan
//	commit-composer --output=FILE [range] # write plan to FILE instead of stdout
//	commit-composer --apply --plan=FILE  # apply a previously-emitted plan
//	commit-composer __sequence-editor X  # internal: used as GIT_SEQUENCE_EDITOR
//	commit-composer __commit-editor X    # internal: used as GIT_EDITOR
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
	"github.com/mrcat71/commit-composer/internal/tui"
	tea "github.com/charmbracelet/bubbletea"
)

// Build-time metadata populated by goreleaser via ldflags:
//
//	-X main.version=<tag> -X main.commit=<sha> -X main.date=<iso8601>
//
// Defaults to "dev" / "" / "" for local builds via `go build` or
// `./scripts/install.sh`.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

func main() {
	// Internal helper dispatch BEFORE flag.Parse so the helper subcommands
	// don't accidentally consume editor-supplied file paths as flags.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "__sequence-editor":
			if err := git.SequenceEditorMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "commit-composer:", err)
				os.Exit(2)
			}
			return
		case "__commit-editor":
			if err := git.CommitEditorMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "commit-composer:", err)
				os.Exit(2)
			}
			return
		case "__reword-editor":
			// Used by the TUI to capture a multi-line reword message: we
			// just print the file path (the TUI shells out to $EDITOR).
			return
		case "__split-prepare":
			if err := splitPrepareMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "commit-composer:", err)
				os.Exit(2)
			}
			return
		case "__review-proposal":
			if err := reviewProposalMain(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "commit-composer:", err)
				os.Exit(2)
			}
			return
		}
	}

	var (
		flagOutput     = flag.String("output", "", "write the structured plan to this file instead of stdout")
		flagApply      = flag.Bool("apply", false, "apply a previously-emitted plan via git rebase -i")
		flagPlanFile   = flag.String("plan", "", "path to a plan file (used with --apply)")
		flagSplitsDir  = flag.String("splits", "", "directory containing <sha>.split.json files for claude-split ops (used with --apply)")
		flagDir        = flag.String("C", "", "run as if commit-composer was started in <path>")
		flagVersion    = flag.Bool("version", false, "print version and exit")
		flagNoColor    = flag.Bool("no-color", false, "disable color output")
		flagListOnly   = flag.Bool("list", false, "print resolved commits and exit (no TUI)")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [<range>]\n\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(os.Stderr, "Range forms:")
		fmt.Fprintln(os.Stderr, "  (omitted)            upstream..HEAD if non-empty, else last 10 commits")
		fmt.Fprintln(os.Stderr, "  HEAD~N               last N commits")
		fmt.Fprintln(os.Stderr, "  <base>..<head>       explicit range")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		flag.PrintDefaults()
	}
	flag.Parse()
	_ = flagNoColor // reserved

	if *flagVersion {
		fmt.Printf("commit-composer %s", version)
		if commit != "" {
			fmt.Printf(" (%s)", commit)
		}
		if date != "" {
			fmt.Printf(" built %s", date)
		}
		fmt.Println()
		// Also include the on-disk path so a user can tell which binary they ran.
		if exe, err := os.Executable(); err == nil {
			fmt.Printf("  binary: %s\n", exe)
		}
		return
	}

	ctx := context.Background()

	repo := git.Repo{Dir: *flagDir}

	if *flagApply {
		if err := runApply(ctx, repo, *flagPlanFile, *flagSplitsDir); err != nil {
			fmt.Fprintln(os.Stderr, "commit-composer:", err)
			os.Exit(1)
		}
		return
	}

	rangeArg := strings.Join(flag.Args(), " ")
	if err := runTUI(ctx, repo, rangeArg, *flagOutput, *flagListOnly); err != nil {
		fmt.Fprintln(os.Stderr, "commit-composer:", err)
		os.Exit(1)
	}
}

func runTUI(ctx context.Context, repo git.Repo, rangeArg, outputPath string, listOnly bool) error {
	base, head, rangeSpec, err := repo.ResolveRange(ctx, rangeArg)
	if err != nil {
		return fmt.Errorf("resolve range: %w", err)
	}
	commits, err := repo.Log(ctx, base, head)
	if err != nil {
		return fmt.Errorf("git log: %w", err)
	}
	if len(commits) == 0 {
		return fmt.Errorf("no commits in range %s", rangeSpec)
	}

	if listOnly {
		for _, c := range commits {
			fmt.Printf("%s %s\n", c.Short, c.Subject)
		}
		return nil
	}

	loadDiff := func(sha string) (string, error) {
		if git.IsUncommitted(sha) {
			return repo.UncommittedDiff(ctx)
		}
		return repo.Diff(ctx, sha)
	}
	loadFiles := func(sha string) ([]git.FileStat, error) {
		if git.IsUncommitted(sha) {
			return repo.UncommittedFiles(ctx)
		}
		return repo.Files(ctx, sha)
	}

	// Auto-include the synthetic working-tree row when the tree is dirty.
	clean, _ := repo.IsClean(ctx)

	m := tui.New(tui.Options{
		Commits:            commits,
		Base:               base,
		RangeSpec:          rangeSpec,
		LoadDiff:           loadDiff,
		LoadFiles:          loadFiles,
		IncludeUncommitted: !clean,
	})

	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr), tea.WithMouseCellMotion())
	final, err := prog.Run()
	if err != nil {
		return fmt.Errorf("tui: %w", err)
	}
	model, ok := final.(tui.Model)
	if !ok {
		return errors.New("tui: unexpected final model type")
	}
	if model.Cancelled() {
		// Cancellation = empty output, exit 0 per the plan contract.
		return nil
	}
	emitted := plan.Marshal(model.Plan())
	if outputPath != "" {
		if err := os.WriteFile(outputPath, []byte(emitted), 0o600); err != nil {
			return fmt.Errorf("write output: %w", err)
		}
		return nil
	}
	_, err = io.WriteString(os.Stdout, emitted)
	return err
}

func runApply(ctx context.Context, repo git.Repo, planFile, splitsDir string) error {
	if planFile == "" {
		return errors.New("--apply requires --plan=FILE")
	}
	f, err := os.Open(planFile)
	if err != nil {
		return fmt.Errorf("open plan: %w", err)
	}
	defer f.Close()
	p, err := plan.Unmarshal(f)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}
	// A dirty working tree is only allowed when the plan includes the
	// synthetic "uncommitted" op - i.e., the user explicitly wants to
	// recompose their dirty tree into commits.
	hasUncommittedOp := false
	for _, op := range p.Ops {
		if git.IsUncommitted(op.SHA) {
			hasUncommittedOp = true
			break
		}
	}
	clean, err := repo.IsClean(ctx)
	if err != nil {
		return fmt.Errorf("check working tree: %w", err)
	}
	if !clean && !hasUncommittedOp {
		return errors.New("working tree has uncommitted changes - commit or stash them, or mark the 'uncommitted' row for recompose in the TUI")
	}
	if clean && hasUncommittedOp {
		return errors.New("plan has an uncommitted-recompose op but the working tree is clean - nothing to recompose")
	}
	self, err := selfPath()
	if err != nil {
		return fmt.Errorf("locate self: %w", err)
	}

	// Skip rebasing the leading run of pass-through picks. For a 192-commit
	// plan where only the last few commits are actually modified, this
	// reduces the rebase to just those few commits - avoiding replays and
	// the conflicts that come with them.
	original := len(p.Ops)
	p = git.TrimLeadingPicks(p)
	if trimmed := original - len(p.Ops); trimmed > 0 {
		fmt.Fprintf(os.Stderr, "commit-composer: trimmed %d leading pass-through commits; rebasing %d commit(s) only\n",
			trimmed, len(p.Ops))
	}
	if len(p.Ops) == 0 {
		fmt.Fprintln(os.Stderr, "commit-composer: nothing to apply - every commit is a pass-through pick")
		return nil
	}

	return repo.Apply(ctx, p, git.ApplyOptions{
		SelfExe:   self,
		SplitsDir: splitsDir,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
}

// poolPrepareEntry is the per-pool manifest entry produced by __split-prepare.
type poolPrepareEntry struct {
	LastSHA     string   `json:"last_sha"`
	PoolSize    int      `json:"pool_size"`
	Commits     []string `json:"commits"`
	Subjects    []string `json:"subjects"`
	DiffPath    string   `json:"diff_path"`
	FilesPath   string   `json:"files_path"`
	HunksPath   string   `json:"hunks_path"`
	CommitsPath string   `json:"commits_path"`
}

// splitPrepareMain is invoked as `commit-composer __split-prepare --plan=FILE
// --out=DIR`. For each claude-recompose POOL it writes:
//
//	<lastSHA>.diff        - unified diff of parent(oldest)..lastSHA for the pool
//	<lastSHA>.files.txt   - name-status lines for every file touched by the pool
//	<lastSHA>.commits.txt - one line per commit in the pool: "<sha> <subject>"
//	manifest.json         - structured list of pools
//
// The slash command reads these, asks Claude to propose groups, then writes
// <lastSHA>.split.json with the approved groups + pool_size for --apply.
func splitPrepareMain(args []string) error {
	fs := flag.NewFlagSet("__split-prepare", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to plan file")
	outDir := fs.String("out", "", "output directory for split-prep artifacts")
	dir := fs.String("C", "", "run as if started in this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *planPath == "" || *outDir == "" {
		return errors.New("__split-prepare: --plan and --out are required")
	}
	if err := os.MkdirAll(*outDir, 0o700); err != nil {
		return fmt.Errorf("mkdir out: %w", err)
	}
	f, err := os.Open(*planPath)
	if err != nil {
		return fmt.Errorf("open plan: %w", err)
	}
	defer f.Close()
	p, err := plan.Unmarshal(f)
	if err != nil {
		return fmt.Errorf("parse plan: %w", err)
	}
	repo := git.Repo{Dir: *dir}
	ctx := context.Background()

	pools := git.RecomposePools(p)
	manifest := make([]poolPrepareEntry, 0, len(pools))

	for last, shas := range pools {
		// Special-case the synthetic working-tree pool: diff is `git diff HEAD`
		// + untracked, not a commit range.
		if git.IsUncommitted(last) {
			diffOut, derr := repo.UncommittedDiff(ctx)
			if derr != nil {
				return fmt.Errorf("uncommitted diff: %w", derr)
			}
			files, ferr := repo.UncommittedFiles(ctx)
			if ferr != nil {
				return fmt.Errorf("uncommitted files: %w", ferr)
			}
			var filesLines []string
			for _, f := range files {
				filesLines = append(filesLines, f.Status+"\t"+f.Path)
			}
			diffPath := filepath.Join(*outDir, git.UncommittedSHA+".diff")
			filesPath := filepath.Join(*outDir, git.UncommittedSHA+".files.txt")
			hunksPath := filepath.Join(*outDir, git.UncommittedSHA+".hunks.json")
			commitsPath := filepath.Join(*outDir, git.UncommittedSHA+".commits.txt")
			if err := os.WriteFile(diffPath, []byte(diffOut), 0o600); err != nil {
				return fmt.Errorf("write uncommitted diff: %w", err)
			}
			if err := os.WriteFile(filesPath, []byte(strings.Join(filesLines, "\n")+"\n"), 0o600); err != nil {
				return fmt.Errorf("write uncommitted files: %w", err)
			}
			if err := writeHunksFile(hunksPath, diffOut); err != nil {
				return fmt.Errorf("write uncommitted hunks: %w", err)
			}
			if err := os.WriteFile(commitsPath, []byte(git.UncommittedSHA+" (uncommitted changes)\n"), 0o600); err != nil {
				return fmt.Errorf("write uncommitted commits: %w", err)
			}
			manifest = append(manifest, poolPrepareEntry{
				LastSHA:     git.UncommittedSHA,
				PoolSize:    0, // 0 marks "not a real pool"
				Commits:     []string{git.UncommittedSHA},
				Subjects:    []string{"(uncommitted changes)"},
				DiffPath:    diffPath,
				FilesPath:   filesPath,
				HunksPath:   hunksPath,
				CommitsPath: commitsPath,
			})
			continue
		}
		oldest := shas[0]
		parent, perr := repo.Run(ctx, "rev-parse", oldest+"^")
		if perr != nil {
			return fmt.Errorf("resolve parent of %s: %w", oldest[:7], perr)
		}
		parent = strings.TrimSpace(parent)
		diffOut, derr := repo.Run(ctx, "diff", "--no-color", parent, last)
		if derr != nil {
			return fmt.Errorf("diff %s..%s: %w", parent[:7], last[:7], derr)
		}
		filesOut, ferr := repo.Run(ctx, "diff", "--name-status", parent, last)
		if ferr != nil {
			return fmt.Errorf("diff name-status %s..%s: %w", parent[:7], last[:7], ferr)
		}

		diffPath := filepath.Join(*outDir, last+".diff")
		filesPath := filepath.Join(*outDir, last+".files.txt")
		hunksPath := filepath.Join(*outDir, last+".hunks.json")
		commitsPath := filepath.Join(*outDir, last+".commits.txt")

		if err := os.WriteFile(diffPath, []byte(diffOut), 0o600); err != nil {
			return fmt.Errorf("write diff: %w", err)
		}
		if err := os.WriteFile(filesPath, []byte(filesOut), 0o600); err != nil {
			return fmt.Errorf("write files: %w", err)
		}
		if err := writeHunksFile(hunksPath, diffOut); err != nil {
			return fmt.Errorf("write hunks: %w", err)
		}

		var commitLines []string
		var subjects []string
		for _, sha := range shas {
			subjOut, _ := repo.Run(ctx, "log", "-1", "--format=%s", sha)
			subj := strings.TrimSpace(subjOut)
			subjects = append(subjects, subj)
			commitLines = append(commitLines, sha+" "+subj)
		}
		if err := os.WriteFile(commitsPath, []byte(strings.Join(commitLines, "\n")+"\n"), 0o600); err != nil {
			return fmt.Errorf("write commits: %w", err)
		}

		manifest = append(manifest, poolPrepareEntry{
			LastSHA:     last,
			PoolSize:    len(shas),
			Commits:     shas,
			Subjects:    subjects,
			DiffPath:    diffPath,
			FilesPath:   filesPath,
			HunksPath:   hunksPath,
			CommitsPath: commitsPath,
		})
	}

	// Sort by LastSHA for stable output.
	for i := 1; i < len(manifest); i++ {
		for j := i; j > 0 && manifest[j-1].LastSHA > manifest[j].LastSHA; j-- {
			manifest[j-1], manifest[j] = manifest[j], manifest[j-1]
		}
	}

	mb, err := jsonMarshalIndent(manifest)
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(*outDir, "manifest.json"), mb, 0o600); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Println(filepath.Join(*outDir, "manifest.json"))
	return nil
}

func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// writeHunksFile parses the given unified diff and writes one JSON-encoded
// Hunk per pool entry to path. Claude consumes this to propose line-level
// splits via {"hunks": [3, 7, 12]} in the resulting split.json.
func writeHunksFile(path, diff string) error {
	hs, err := git.ParseHunks(diff)
	if err != nil {
		return err
	}
	if hs == nil {
		hs = []git.Hunk{}
	}
	data, err := json.MarshalIndent(hs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// reviewProposalMain is invoked as `commit-composer __review-proposal
// --splits=DIR [--output=FILE]`. It loads every <sha>.split.json in DIR,
// presents them in the review TUI, then writes the revised proposals back
// to disk and prints a summary JSON outcome.
//
// The summary tells the slash command:
//   accept: true if Enter pressed, false on q/esc
//   has_comments: true if any group has a non-empty comment
//   groups_changed: true if the user edited groups (reword/squash/drop)
//   pools: the revised proposal in full
func reviewProposalMain(args []string) error {
	fs := flag.NewFlagSet("__review-proposal", flag.ContinueOnError)
	splitsDir := fs.String("splits", "", "directory with <sha>.split.json proposal files")
	outFile := fs.String("output", "", "write the outcome JSON here (default: stdout)")
	repoDir := fs.String("C", "", "run as if started in this directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *splitsDir == "" {
		return errors.New("__review-proposal: --splits is required")
	}

	pools, err := loadPoolsFromSplits(*splitsDir)
	if err != nil {
		return err
	}
	if len(pools) == 0 {
		return errors.New("__review-proposal: no proposals found in splits dir")
	}

	ctx := context.Background()
	repo := git.Repo{Dir: *repoDir}
	loadDiff := func(poolSHA string, poolSize int, files []string) (string, error) {
		// Synthetic working-tree pool: show staged + unstaged + untracked
		// limited to the files in this group.
		if git.IsUncommitted(poolSHA) {
			out, err := repo.UncommittedDiff(ctx)
			if err != nil {
				return "", err
			}
			return filterDiffByFiles(out, files), nil
		}
		// Real pool: diff <parent-of-first-commit-in-pool>..<last-commit-of-pool>.
		size := poolSize
		if size < 1 {
			size = 1
		}
		parent, err := repo.ParentOrEmpty(ctx, poolSHA, size)
		if err != nil {
			return "", err
		}
		return repo.DiffPaths(ctx, parent, poolSHA, files)
	}

	m := tui.NewReview(tui.ReviewOptions{Pools: pools, RepoDir: *repoDir, LoadDiff: loadDiff})
	prog := tea.NewProgram(m, tea.WithAltScreen(), tea.WithOutput(os.Stderr), tea.WithMouseCellMotion())
	final, err := prog.Run()
	if err != nil {
		return fmt.Errorf("review tui: %w", err)
	}
	rm, ok := final.(tui.ReviewModel)
	if !ok {
		return errors.New("review tui: unexpected final model type")
	}

	payload, err := rm.MarshalOutcome()
	if err != nil {
		return fmt.Errorf("marshal outcome: %w", err)
	}

	// Persist revised pools back to <sha>.split.json for the apply phase.
	if rm.Outcome().Accept {
		if err := writeRevisedPools(*splitsDir, rm.RevisedPools()); err != nil {
			return fmt.Errorf("write revised pools: %w", err)
		}
	}

	if *outFile != "" {
		return os.WriteFile(*outFile, payload, 0o600)
	}
	_, err = os.Stdout.Write(payload)
	return err
}

// loadPoolsFromSplits reads every *.split.json in dir and returns the
// in-memory pool list. Sorted by SHA for deterministic order.
func loadPoolsFromSplits(dir string) ([]tui.ProposalPool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read splits dir: %w", err)
	}
	var pools []tui.ProposalPool
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".split.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		// On-disk schema mirrors git.SplitSpec; copy into the TUI's
		// ProposalPool which adds per-group comments.
		var spec struct {
			SHA      string `json:"sha"`
			PoolSize int    `json:"pool_size"`
			Groups   []struct {
				Files   []string `json:"files"`
				Hunks   []int    `json:"hunks,omitempty"`
				Message string   `json:"message"`
				Comment string   `json:"comment,omitempty"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(data, &spec); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if spec.PoolSize == 0 {
			spec.PoolSize = 1
		}
		groups := make([]tui.ProposalGroup, len(spec.Groups))
		for i, g := range spec.Groups {
			groups[i] = tui.ProposalGroup{Files: g.Files, Hunks: g.Hunks, Message: g.Message, Comment: g.Comment}
		}
		pools = append(pools, tui.ProposalPool{
			SHA:      spec.SHA,
			PoolSize: spec.PoolSize,
			Groups:   groups,
		})
	}
	// Stable sort by SHA.
	for i := 1; i < len(pools); i++ {
		for j := i; j > 0 && pools[j-1].SHA > pools[j].SHA; j-- {
			pools[j-1], pools[j] = pools[j], pools[j-1]
		}
	}
	return pools, nil
}

func writeRevisedPools(dir string, pools []tui.ProposalPool) error {
	type outGroup struct {
		Files   []string `json:"files"`
		Hunks   []int    `json:"hunks,omitempty"`
		Message string   `json:"message"`
		Comment string   `json:"comment,omitempty"`
	}
	for _, p := range pools {
		path := filepath.Join(dir, p.SHA+".split.json")
		out := struct {
			SHA      string     `json:"sha"`
			PoolSize int        `json:"pool_size"`
			Groups   []outGroup `json:"groups"`
		}{
			SHA:      p.SHA,
			PoolSize: p.PoolSize,
		}
		for _, g := range p.Groups {
			out.Groups = append(out.Groups, outGroup{
				Files:   g.Files,
				Hunks:   g.Hunks,
				Message: g.Message,
				Comment: g.Comment,
			})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// filterDiffByFiles keeps only the per-file blocks of a unified diff that
// match one of paths. Used to scope the working-tree diff to a proposed
// group's file set in the review TUI.
func filterDiffByFiles(diff string, paths []string) string {
	if len(paths) == 0 {
		return diff
	}
	keep := make(map[string]bool, len(paths))
	for _, p := range paths {
		keep[p] = true
	}
	var out strings.Builder
	var cur strings.Builder
	var include bool
	flush := func() {
		if include {
			out.WriteString(cur.String())
		}
		cur.Reset()
		include = false
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			// Parse `diff --git a/<old> b/<new>`. Use the b/ path as the file id.
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				newPath := strings.TrimPrefix(parts[3], "b/")
				if keep[newPath] {
					include = true
				}
			}
		}
		cur.WriteString(line)
	}
	flush()
	return out.String()
}

func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	// Resolve symlinks so the path passed to git as GIT_SEQUENCE_EDITOR is
	// stable across PATH lookups.
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

