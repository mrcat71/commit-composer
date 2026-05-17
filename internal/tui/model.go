// Package tui implements the bubbletea TUI for commit-composer.
//
// Layout is a split pane: commit list on the left (with action tags), and a
// details/diff viewer on the right. Vim-style navigation, action keys for
// pick/reword/squash/fixup/drop/edit, J/K to reorder.
package tui

import (
	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// row pairs a commit with its current Action and the index it occupied when
// the TUI started (used to detect reorder).
type row struct {
	commit    git.Commit
	action    plan.Action
	origIndex int
	// reword stores the new message when the user reworded the commit;
	// empty if the user has not reworded yet.
	reword string
}

// Model is the bubbletea Model for the commit-composer TUI.
//
// Exported only so the cmd entry point can construct an initial Model;
// internal fields stay package-private.
type Model struct {
	rows []row

	// Cursor in the commit list.
	cursor int
	// listOffset is the index of the first commit shown in the left pane.
	// Updated edge-triggered on cursor move so we scroll only when the
	// cursor would otherwise fall off the viewport.
	listOffset int

	// focus tracks which pane receives j/k input. 0 = commits list (left),
	// 1 = diff (right). Tab cycles; h focuses left, l focuses right.
	focus int

	// Layout
	width  int
	height int

	// Diff viewer for the right pane (scrollable).
	diff viewport.Model
	// lastDiffSHA caches the SHA currently rendered in the diff viewport so
	// we don't re-shell-out to git on every cursor move when scrolling.
	lastDiffSHA string

	// Loader for full diffs; populated by the command entry point.
	loadDiff func(sha string) (string, error)
	// diffCache holds the raw diff text per SHA so navigation back to a
	// previously-loaded commit is instant. Grows as the user moves around.
	diffCache map[string]string

	// Loader for the files list (also lazy).
	loadFiles  func(sha string) ([]git.FileStat, error)
	filesCache map[string][]git.FileStat

	// Plan metadata (base + range) carried through to the emitted Plan.
	base       string
	rangeSpec  string

	// Cancelled is true when the user pressed q / esc / ctrl+c.
	cancelled bool

	// Help overlay visible.
	showHelp bool

	// Status line text (errors, hints).
	status      string
	statusError bool

	keys   keymap
	styles styles
}

// Options carries the inputs needed to construct a Model.
type Options struct {
	Commits   []git.Commit
	Base      string
	RangeSpec string

	// LoadDiff is called lazily when the cursor moves to a new commit. Must be
	// safe to call repeatedly with the same SHA. For the synthetic WORKING
	// row, the implementation should ignore the sha argument and return the
	// working-tree diff via git.UncommittedDiff.
	LoadDiff func(sha string) (string, error)
	// LoadFiles returns the name-status list for a SHA. Same WORKING note as
	// LoadDiff applies.
	LoadFiles func(sha string) ([]git.FileStat, error)

	// IncludeUncommitted, when true, prepends a virtual "WORKING" row at the
	// top of the commit list representing staged + unstaged + untracked
	// changes vs HEAD. Only Pick (default no-op) and ClaudeRecompose actions
	// are accepted on that row.
	IncludeUncommitted bool
}

// New constructs an initial Model. The caller passes Commits in oldest-first
// order (as `git log --reverse` returns them). The TUI displays them in
// newest-first order (top of the list = most recent commit, matching Fork /
// `git log`'s default). On Plan() emission the order is reversed back so the
// rebase todo gets oldest-first as `git rebase -i` requires.
//
// OrigIndex on each row stores the original oldest-first position so the
// reorder detection still works after the display flip.
func New(opts Options) Model {
	n := len(opts.Commits)
	rows := make([]row, n)
	for i, c := range opts.Commits {
		rows[n-1-i] = row{commit: c, action: plan.Pick, origIndex: i}
	}
	if opts.IncludeUncommitted {
		// Prepend a synthetic "uncommitted changes" row at the top
		// (newest-first display). origIndex = -1 marks it as virtual.
		uncommitted := row{
			commit: git.Commit{
				SHA:     git.UncommittedSHA,
				Short:   "WORKING",
				Subject: "(uncommitted changes)",
				// Use a far-future date so the relative-date column shows
				// nothing distracting; renderRow handles WORKING specially.
			},
			action:    plan.Pick,
			origIndex: -1,
		}
		rows = append([]row{uncommitted}, rows...)
	}
	vp := viewport.New(40, 10) // resized on first WindowSizeMsg
	return Model{
		rows:       rows,
		diff:       vp,
		loadDiff:   opts.LoadDiff,
		loadFiles:  opts.LoadFiles,
		filesCache: make(map[string][]git.FileStat),
		diffCache:  make(map[string]string),
		base:       opts.Base,
		rangeSpec:  opts.RangeSpec,
		keys:       newKeymap(),
		styles:     newStyles(),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return nil
}

// Plan returns the user's marked plan in **rebase-todo order** (oldest-first),
// even though the TUI displays rows in newest-first order.
//
// The synthetic WORKING row (if present) is emitted only when actively marked
// (Action != Pick). Otherwise it's filtered out so downstream code doesn't
// have to special-case a no-op WORKING entry.
func (m Model) Plan() plan.Plan {
	type pending struct {
		op       plan.Op
		isWorking bool
	}
	var keep []pending
	for _, r := range m.rows {
		if r.commit.SHA == git.UncommittedSHA && r.action == plan.Pick {
			continue // no-op: don't touch the working tree
		}
		op := plan.Op{
			SHA:       r.commit.SHA,
			Action:    r.action,
			OrigIndex: r.origIndex,
		}
		if r.action == plan.Reword {
			op.NewMessage = r.reword
		}
		keep = append(keep, pending{op: op, isWorking: r.commit.SHA == git.UncommittedSHA})
	}
	// Reverse so the result is in rebase-todo order (oldest-first), then
	// move any WORKING op to the very END (it's applied AFTER the rebase).
	n := len(keep)
	ops := make([]plan.Op, 0, n)
	var working *plan.Op
	for i := n - 1; i >= 0; i-- {
		p := keep[i]
		if p.isWorking {
			w := p.op
			working = &w
			continue
		}
		ops = append(ops, p.op)
	}
	if working != nil {
		ops = append(ops, *working)
	}
	return plan.Plan{
		Base:  m.base,
		Range: m.rangeSpec,
		Ops:   ops,
	}
}

// Cancelled reports whether the user dismissed the TUI without confirming.
func (m Model) Cancelled() bool {
	return m.cancelled
}
