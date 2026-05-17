package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// rewordMsg carries the result of running $EDITOR to capture a multi-line
// reword message.
type rewordMsg struct {
	index   int
	message string
	err     error
}

// rewordCmd suspends the TUI, opens $EDITOR with the existing message as
// initial content, then resumes and dispatches a rewordMsg with the new
// content.
func rewordCmd(idx int, initial string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	// Write the initial message to a temp file ahead of suspending.
	tmp, err := os.CreateTemp("", "commit-composer-reword-*.txt")
	if err != nil {
		return func() tea.Msg { return rewordMsg{index: idx, err: err} }
	}
	path := tmp.Name()
	if _, err := tmp.WriteString(initial); err != nil {
		tmp.Close()
		os.Remove(path)
		return func() tea.Msg { return rewordMsg{index: idx, err: err} }
	}
	tmp.Close()

	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(execErr error) tea.Msg {
		defer os.Remove(path)
		if execErr != nil {
			return rewordMsg{index: idx, err: execErr}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return rewordMsg{index: idx, err: err}
		}
		return rewordMsg{index: idx, message: string(data)}
	})
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg), m.loadDiffCmd()
	case tea.KeyMsg:
		return m.handleKey(msg)
	case diffLoadedMsg:
		return m.handleDiffLoaded(msg), nil
	case rewordMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("reword failed: %v", msg.err)
			m.statusError = true
			return m, nil
		}
		if msg.index >= 0 && msg.index < len(m.rows) {
			m.rows[msg.index].action = plan.Reword
			m.rows[msg.index].reword = strings.TrimRight(msg.message, "\n")
			m.status = "reword saved"
			m.statusError = false
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.diff, cmd = m.diff.Update(msg)
	return m, cmd
}

// isWorkingRow reports whether the row at the given index is the synthetic
// uncommitted-changes row. Reorder / non-recompose actions check this to
// avoid moving it or applying nonsensical actions.
func (m Model) isWorkingRow(i int) bool {
	return i >= 0 && i < len(m.rows) && m.rows[i].commit.SHA == git.UncommittedSHA
}

// resetDiffViewport refreshes the right pane for the cursor's commit.
// If we have the diff cached, paint it instantly; otherwise show a
// "loading" placeholder while the async load runs.
func (m *Model) resetDiffViewport() {
	if len(m.rows) == 0 {
		return
	}
	sha := m.rows[m.cursor].commit.SHA
	if sha == m.lastDiffSHA {
		return
	}
	if cached, ok := m.diffCache[sha]; ok {
		m.diff.SetContent(colorizeDiff(cached, m.styles))
		m.diff.GotoTop()
		m.lastDiffSHA = sha
		return
	}
	m.diff.SetContent(m.styles.help.Render(fmt.Sprintf("loading diff for %s...", sha[:min(7, len(sha))])))
	m.diff.GotoTop()
	m.lastDiffSHA = ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ensureCursorVisible adjusts listOffset edge-triggered: only when the cursor
// would otherwise fall outside the current viewport. Vim-style scrolling.
func (m *Model) ensureCursorVisible() {
	h := m.listViewportHeight()
	if h <= 0 {
		return
	}
	if m.cursor < m.listOffset {
		m.listOffset = m.cursor
	}
	if m.cursor >= m.listOffset+h {
		m.listOffset = m.cursor - h + 1
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

func (m Model) handleResize(msg tea.WindowSizeMsg) Model {
	m.width = msg.Width
	m.height = msg.Height
	m.ensureCursorVisible()
	// The diff viewport's exact size is computed in renderDetails on every
	// render, since the header height depends on the current commit's files
	// and message length. Set a conservative default here so initial layout
	// works before the first render.
	_, right := m.paneWidths()
	m.diff.Width = right - 4
	if m.diff.Width < 10 {
		m.diff.Width = 10
	}
	available := m.bodyHeight() - 2
	if available < 5 {
		available = 5
	}
	m.diff.Height = available / 2
	if m.diff.Height < 3 {
		m.diff.Height = 3
	}
	return m
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	// Help overlay swallows all keys except dismiss.
	if m.showHelp {
		switch msg.String() {
		case "?", "esc", "q":
			m.showHelp = false
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Cancel):
		m.cancelled = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Confirm):
		// Plan validation: at least one non-drop operation must remain, and
		// the first kept commit cannot be a squash/fixup (nothing to fold into).
		if err := validate(m.rows); err != nil {
			m.status = err.Error()
			m.statusError = true
			return m, nil
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.showHelp = true
		return m, nil

	// Pane focus switching.
	case key.Matches(msg, m.keys.FocusToggle):
		m.focus = 1 - m.focus
		return m, nil
	case key.Matches(msg, m.keys.FocusLeft):
		m.focus = 0
		return m, nil
	case key.Matches(msg, m.keys.FocusRight):
		m.focus = 1
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.focus == 1 {
			m.diff.LineUp(1)
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
			m.ensureCursorVisible()
			m.resetDiffViewport()
		}
		return m, m.loadDiffCmd()
	case key.Matches(msg, m.keys.Down):
		if m.focus == 1 {
			m.diff.LineDown(1)
			return m, nil
		}
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.ensureCursorVisible()
			m.resetDiffViewport()
		}
		return m, m.loadDiffCmd()
	case key.Matches(msg, m.keys.Top):
		if m.focus == 1 {
			m.diff.GotoTop()
			return m, nil
		}
		if len(m.rows) > 0 {
			m.cursor = 0
			m.ensureCursorVisible()
			m.resetDiffViewport()
		}
		return m, m.loadDiffCmd()
	case key.Matches(msg, m.keys.Bottom):
		if m.focus == 1 {
			m.diff.GotoBottom()
			return m, nil
		}
		if len(m.rows) > 0 {
			m.cursor = len(m.rows) - 1
			m.ensureCursorVisible()
			m.resetDiffViewport()
		}
		return m, m.loadDiffCmd()

	case key.Matches(msg, m.keys.MoveUp):
		if m.cursor > 0 && !m.isWorkingRow(m.cursor) && !m.isWorkingRow(m.cursor-1) {
			m.rows[m.cursor], m.rows[m.cursor-1] = m.rows[m.cursor-1], m.rows[m.cursor]
			m.cursor--
			m.ensureCursorVisible()
			m.status = "moved up"
			m.statusError = false
		}
		return m, nil
	case key.Matches(msg, m.keys.MoveDown):
		if m.cursor < len(m.rows)-1 && !m.isWorkingRow(m.cursor) && !m.isWorkingRow(m.cursor+1) {
			m.rows[m.cursor], m.rows[m.cursor+1] = m.rows[m.cursor+1], m.rows[m.cursor]
			m.cursor++
			m.ensureCursorVisible()
			m.status = "moved down"
			m.statusError = false
		}
		return m, nil

	case key.Matches(msg, m.keys.Pick):
		return m.setAction(plan.Pick), nil
	case key.Matches(msg, m.keys.Squash):
		return m.setAction(plan.Squash), nil
	case key.Matches(msg, m.keys.Fixup):
		return m.setAction(plan.Fixup), nil
	case key.Matches(msg, m.keys.Drop):
		return m.setAction(plan.Drop), nil
	case key.Matches(msg, m.keys.Edit):
		return m.setAction(plan.Edit), nil
	case key.Matches(msg, m.keys.ClaudeRecompose):
		return m.toggleRecompose(), nil
	case key.Matches(msg, m.keys.Reword):
		if len(m.rows) == 0 {
			return m, nil
		}
		r := m.rows[m.cursor]
		initial := r.reword
		if initial == "" {
			initial = r.commit.Message()
		}
		return m, rewordCmd(m.cursor, initial)

	case key.Matches(msg, m.keys.Cycle):
		return m.cycleAction(), nil

	case key.Matches(msg, m.keys.ScrollUp):
		m.diff.LineUp(1)
		return m, nil
	case key.Matches(msg, m.keys.ScrollDown):
		m.diff.LineDown(1)
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		// PageUp routes by focus: scroll the diff when focused on the right,
		// otherwise scroll the commit list by a page.
		if m.focus == 1 {
			m.diff.HalfViewUp()
			return m, nil
		}
		page := m.listViewportHeight()
		if page < 1 {
			page = 1
		}
		m.cursor -= page
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.ensureCursorVisible()
		m.resetDiffViewport()
		return m, m.loadDiffCmd()
	case key.Matches(msg, m.keys.PageDown):
		if m.focus == 1 {
			m.diff.HalfViewDown()
			return m, nil
		}
		page := m.listViewportHeight()
		if page < 1 {
			page = 1
		}
		m.cursor += page
		if m.cursor > len(m.rows)-1 {
			m.cursor = len(m.rows) - 1
		}
		m.ensureCursorVisible()
		m.resetDiffViewport()
		return m, m.loadDiffCmd()
	}

	return m, nil
}

func (m Model) setAction(a plan.Action) Model {
	if len(m.rows) == 0 {
		return m
	}
	r := &m.rows[m.cursor]
	if r.commit.SHA == git.UncommittedSHA && a != plan.Pick && a != plan.ClaudeRecompose {
		m.status = "uncommitted row only accepts pick / recompose"
		m.statusError = true
		return m
	}
	r.action = a
	m.status = fmt.Sprintf("set %s", a)
	m.statusError = false
	return m
}

// cycleOrder controls the Space-key cycle. ClaudeRecompose comes right after
// Pick so the primary use-case (mark for recompose) is one keystroke away
// from the default. The remaining rebase actions follow in the order users
// typically reach for them.
var cycleOrder = []plan.Action{
	plan.Pick,
	plan.ClaudeRecompose,
	plan.Reword,
	plan.Squash,
	plan.Fixup,
	plan.Drop,
	plan.Edit,
}

func (m Model) cycleAction() Model {
	if len(m.rows) == 0 {
		return m
	}
	r := &m.rows[m.cursor]
	if r.commit.SHA == git.UncommittedSHA {
		// WORKING row toggles only between Pick and ClaudeRecompose.
		if r.action == plan.Pick {
			r.action = plan.ClaudeRecompose
		} else {
			r.action = plan.Pick
		}
		m.status = fmt.Sprintf("uncommitted -> %s", r.action)
		m.statusError = false
		return m
	}
	idx := 0
	for i, a := range cycleOrder {
		if a == r.action {
			idx = i
			break
		}
	}
	next := cycleOrder[(idx+1)%len(cycleOrder)]
	r.action = next
	m.status = fmt.Sprintf("cycled -> %s", next)
	m.statusError = false
	return m
}

// toggleRecompose flips the highlighted row between Pick and ClaudeRecompose.
// Consecutive ClaudeRecompose rows are pooled by the apply phase, so this is
// the only state we need to track here.
func (m Model) toggleRecompose() Model {
	if len(m.rows) == 0 {
		return m
	}
	r := &m.rows[m.cursor]
	if r.action == plan.ClaudeRecompose {
		r.action = plan.Pick
		m.status = "unmarked (pick)"
	} else {
		r.action = plan.ClaudeRecompose
		m.status = m.poolStatus()
	}
	m.statusError = false
	return m
}

// poolStatus reports the size of the pool the cursor's row currently belongs
// to (i.e., the run of consecutive ClaudeRecompose rows that includes it).
func (m Model) poolStatus() string {
	first, last := m.poolBounds(m.cursor)
	n := last - first
	if n <= 1 {
		return "recompose: 1 commit in this group"
	}
	return fmt.Sprintf("recompose: %d commits in this group", n)
}

// poolBounds returns the [first, last) range of rows in the same pool as i.
// If row i is not marked ClaudeRecompose, returns (i, i).
func (m Model) poolBounds(i int) (int, int) {
	if i < 0 || i >= len(m.rows) || m.rows[i].action != plan.ClaudeRecompose {
		return i, i
	}
	first := i
	for first > 0 && m.rows[first-1].action == plan.ClaudeRecompose {
		first--
	}
	last := i + 1
	for last < len(m.rows) && m.rows[last].action == plan.ClaudeRecompose {
		last++
	}
	return first, last
}

// loadDiffCmd refreshes the right-pane diff for the commit under the cursor.
// Cache hit -> nil (already populated synchronously by resetDiffViewport).
// Cache miss -> async load, content fills in via diffLoadedMsg.
func (m Model) loadDiffCmd() tea.Cmd {
	if len(m.rows) == 0 || m.loadDiff == nil {
		return nil
	}
	sha := m.rows[m.cursor].commit.SHA
	if sha == m.lastDiffSHA && m.diff.View() != "" {
		return nil
	}
	if _, ok := m.diffCache[sha]; ok {
		// Cache hit - nothing to do; resetDiffViewport already painted it.
		return nil
	}
	return func() tea.Msg {
		out, err := m.loadDiff(sha)
		if err != nil {
			return diffLoadedMsg{sha: sha, err: err}
		}
		return diffLoadedMsg{sha: sha, content: out}
	}
}

type diffLoadedMsg struct {
	sha     string
	content string
	err     error
}

func (m Model) handleDiffLoaded(msg diffLoadedMsg) Model {
	if msg.err == nil && msg.sha != "" {
		// Cache successful loads so revisiting is instant.
		m.diffCache[msg.sha] = msg.content
	}
	// Only apply to the viewport if the cursor is still on this SHA.
	curSHA := ""
	if len(m.rows) > 0 {
		curSHA = m.rows[m.cursor].commit.SHA
	}
	if msg.sha != curSHA {
		return m // user moved on - cache is enough
	}
	if msg.err != nil {
		m.diff.SetContent(fmt.Sprintf("error loading diff: %v", msg.err))
		m.lastDiffSHA = msg.sha
		return m
	}
	m.diff.SetContent(colorizeDiff(msg.content, m.styles))
	m.diff.GotoTop()
	m.lastDiffSHA = msg.sha
	return m
}

// validate ensures the plan would produce a syntactically valid rebase todo.
//
// Rows are stored newest-first (display order), but the rebase applies them
// oldest-first - so the "earliest applied commit" we care about for the
// "can't squash/fixup the first commit" check is the LAST non-dropped row in
// the slice.
func validate(rows []row) error {
	earliestKept := -1
	allDropped := true
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		if r.action == plan.Drop {
			continue
		}
		allDropped = false
		if earliestKept < 0 {
			earliestKept = i
		}
	}
	if allDropped {
		return fmt.Errorf("plan drops every commit - rebase would have nothing to apply")
	}
	if r := rows[earliestKept]; r.action == plan.Squash || r.action == plan.Fixup {
		return fmt.Errorf("earliest kept commit %s is %s but has nothing to fold into", r.commit.Short, r.action)
	}
	return nil
}
