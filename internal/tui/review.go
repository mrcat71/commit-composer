package tui

// review.go - the second-pass TUI that shows Claude's proposed commits as
// virtual rows so the user can edit them OR leave inline comments that
// trigger a Claude revision round.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/mrcat71/commit-composer/internal/git"
)

// ProposalPool is one pool's proposal, mirroring the on-disk SplitSpec but
// with comments attached.
type ProposalPool struct {
	SHA      string          `json:"sha"`       // last commit of the original pool
	PoolSize int             `json:"pool_size"` // how many originals were dissolved
	Commits  []string        `json:"commits,omitempty"`
	Groups   []ProposalGroup `json:"groups"`
}

// ProposalGroup is one virtual commit in a proposal.
//
// Files is the legacy file-level scope. Hunks is the line-level scope; when
// non-empty the apply step uses only the indexed hunks of the pool's diff,
// ignoring Files entirely.
type ProposalGroup struct {
	Files   []string `json:"files"`
	Hunks   []int    `json:"hunks,omitempty"`
	Message string   `json:"message"`
	Comment string   `json:"comment,omitempty"` // populated when user leaves a note
}

// ReviewOutcome is what the binary prints to stdout after the review TUI
// exits. The slash command parses it to decide what to do next.
type ReviewOutcome struct {
	// Accept is true if the user pressed Enter to submit.
	// false on q / ctrl+c.
	Accept bool `json:"accept"`
	// HasComments is true if any group has a non-empty Comment OR there's a
	// pool-level GlobalComment. Slash command sees this and routes back to
	// Claude for revision.
	HasComments bool `json:"has_comments"`
	// GroupsChanged is true if the user edited group structure
	// (reword/squash/drop) - vs only adding comments.
	GroupsChanged bool `json:"groups_changed"`
}

// reviewRow is one virtual commit row in the second-pass TUI.
type reviewRow struct {
	poolIdx int // index into ReviewModel.pools
	// gIdx is the index of this row's group in pools[poolIdx].Groups at
	// load time. After squash/drop it can still point at the original
	// slot; the active group index is resolved at render via poolGroups.
	gIdx int

	files       []string
	hunks       []int // indices into the pool's hunk list (line-level scope)
	message     string
	origMessage string // for change detection
	comment     string
	dropped     bool // d: merged into the previous group within pool
	squashed    bool // s: same as dropped but keeps a marker in message
}

// ReviewModel renders the proposal-review TUI.
type ReviewModel struct {
	pools []ProposalPool

	rows       []reviewRow
	cursor     int
	listOffset int

	// focus: 0 = proposal list (left), 1 = diff viewport (right). Tab cycles.
	focus int

	width  int
	height int
	diff   viewport.Model
	// diffCache is keyed by poolSHA + "\x00" + strings.Join(files, "\x00")
	// so adjacent groups touching the same files do not re-shell out to git.
	diffCache  map[string]string
	currentKey string // cache key currently rendered in the diff viewport

	keys   reviewKeymap
	styles styles

	cancelled bool
	status    string
	statusErr bool

	// loadDiff returns the unified-diff text for the given pool and file set.
	// Wired by the cmd entry point; nil keeps the diff pane empty.
	loadDiff func(poolSHA string, poolSize int, files []string) (string, error)

	// repoDir lets us resolve original SHAs to diffs if the user wants to
	// see what a proposed group's files actually look like in HEAD~.
	repoDir string
}

type reviewKeymap struct {
	Up       key.Binding
	Down     key.Binding
	Top      key.Binding
	Bottom   key.Binding
	PageUp   key.Binding
	PageDown key.Binding

	Reword  key.Binding
	Squash  key.Binding
	Drop    key.Binding
	Comment key.Binding

	Focus key.Binding

	Confirm key.Binding
	Cancel  key.Binding
	Help    key.Binding
}

func newReviewKeymap() reviewKeymap {
	return reviewKeymap{
		Up:       key.NewBinding(key.WithKeys("k", "up")),
		Down:     key.NewBinding(key.WithKeys("j", "down")),
		Top:      key.NewBinding(key.WithKeys("g", "home")),
		Bottom:   key.NewBinding(key.WithKeys("G", "end")),
		PageUp:   key.NewBinding(key.WithKeys("ctrl+u", "pgup")),
		PageDown: key.NewBinding(key.WithKeys("ctrl+d", "pgdown")),

		Reword:  key.NewBinding(key.WithKeys("r")),
		Squash:  key.NewBinding(key.WithKeys("s")),
		Drop:    key.NewBinding(key.WithKeys("d")),
		Comment: key.NewBinding(key.WithKeys("m")),

		Focus: key.NewBinding(key.WithKeys("tab", "shift+tab")),

		Confirm: key.NewBinding(key.WithKeys("enter")),
		Cancel:  key.NewBinding(key.WithKeys("q", "ctrl+c", "esc")),
		Help:    key.NewBinding(key.WithKeys("?")),
	}
}

// ReviewOptions wires inputs into ReviewModel.
type ReviewOptions struct {
	Pools   []ProposalPool
	RepoDir string
	// LoadDiff is called lazily when the cursor moves to a row whose
	// (pool, files) combination has not been seen yet. Returns the unified
	// diff body. nil means the right pane just shows metadata, no diff.
	LoadDiff func(poolSHA string, poolSize int, files []string) (string, error)
}

// NewReview builds the model from a list of pools.
func NewReview(opts ReviewOptions) ReviewModel {
	var rows []reviewRow
	for pi, p := range opts.Pools {
		for gi, g := range p.Groups {
			rows = append(rows, reviewRow{
				poolIdx:     pi,
				gIdx:        gi,
				files:       append([]string(nil), g.Files...),
				hunks:       append([]int(nil), g.Hunks...),
				message:     g.Message,
				origMessage: g.Message,
				comment:     g.Comment,
			})
		}
	}
	vp := viewport.New(viewport.WithWidth(40), viewport.WithHeight(10))
	return ReviewModel{
		pools:     opts.Pools,
		rows:      rows,
		diff:      vp,
		diffCache: make(map[string]string),
		loadDiff:  opts.LoadDiff,
		keys:      newReviewKeymap(),
		styles:    newStyles(),
		repoDir:   opts.RepoDir,
	}
}

// Init implements tea.Model.
func (m ReviewModel) Init() tea.Cmd { return nil }

// Cancelled reports the exit reason.
func (m ReviewModel) Cancelled() bool { return m.cancelled }

// Outcome returns the structured result the slash command consumes.
func (m ReviewModel) Outcome() ReviewOutcome {
	out := ReviewOutcome{Accept: !m.cancelled}
	for _, r := range m.rows {
		if r.comment != "" {
			out.HasComments = true
		}
		if r.dropped || r.squashed || r.message != r.origMessage {
			out.GroupsChanged = true
		}
	}
	return out
}

// RevisedPools returns the user-edited pool list with squash/drop applied.
// Files from dropped/squashed rows fold into the previous active row in the
// same pool. Messages from squash are appended; from drop are discarded.
func (m ReviewModel) RevisedPools() []ProposalPool {
	out := make([]ProposalPool, len(m.pools))
	for i, p := range m.pools {
		out[i] = ProposalPool{
			SHA:      p.SHA,
			PoolSize: p.PoolSize,
			Commits:  p.Commits,
		}
	}
	for _, r := range m.rows {
		if r.dropped || r.squashed {
			continue
		}
		g := ProposalGroup{
			Files:   append([]string(nil), r.files...),
			Hunks:   append([]int(nil), r.hunks...),
			Message: r.message,
			Comment: r.comment,
		}
		out[r.poolIdx].Groups = append(out[r.poolIdx].Groups, g)
	}
	// Fold dropped/squashed rows into the preceding active row in their pool.
	// This is a second pass so the indices stay coherent during the first pass.
	for i, r := range m.rows {
		if !r.dropped && !r.squashed {
			continue
		}
		// Find the last active row in the same pool that came before i.
		prev := -1
		for j := i - 1; j >= 0; j-- {
			if m.rows[j].poolIdx != r.poolIdx {
				break
			}
			if !m.rows[j].dropped && !m.rows[j].squashed {
				prev = j
				break
			}
		}
		if prev < 0 {
			// No previous active row in this pool - the dropped row's files
			// have nowhere to land. Leave it in the output as a standalone
			// group; the apply phase will reject if files aren't covered.
			out[r.poolIdx].Groups = append(out[r.poolIdx].Groups, ProposalGroup{
				Files:   r.files,
				Message: r.message,
				Comment: r.comment,
			})
			continue
		}
		// Append files. For squash, append the message; for drop, discard
		// this row's message but keep the comment so Claude sees it.
		target := findGroupForRow(out, m.rows[prev])
		if target == nil {
			continue
		}
		target.Files = append(target.Files, r.files...)
		target.Hunks = append(target.Hunks, r.hunks...)
		if r.squashed && strings.TrimSpace(r.message) != "" {
			target.Message = strings.TrimRight(target.Message, "\n") + "\n\n" + r.message
		}
		if r.comment != "" {
			if target.Comment != "" {
				target.Comment += "\n---\n"
			}
			target.Comment += r.comment
		}
	}
	return out
}

// findGroupForRow walks out[].Groups and returns the pointer to the group
// originally produced by the given row. Compares by Message identity which
// is sufficient because the first pass preserved Message and Files.
func findGroupForRow(pools []ProposalPool, r reviewRow) *ProposalGroup {
	for i := range pools[r.poolIdx].Groups {
		g := &pools[r.poolIdx].Groups[i]
		if g.Message == r.message {
			return g
		}
	}
	return nil
}

// MarshalOutcome returns the JSON payload to write to stdout. Includes the
// summary outcome plus the revised proposal so the slash command can update
// the per-pool spec files on disk in one read.
func (m ReviewModel) MarshalOutcome() ([]byte, error) {
	payload := struct {
		ReviewOutcome
		Pools []ProposalPool `json:"pools"`
	}{
		ReviewOutcome: m.Outcome(),
		Pools:         m.RevisedPools(),
	}
	return json.MarshalIndent(payload, "", "  ")
}

// Update implements tea.Model.
func (m ReviewModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, m.maybeLoadDiff()
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		m2, cmd := m.handleMouse(msg)
		return m2, cmd
	case reviewRewordMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("reword failed: %v", msg.err)
			m.statusErr = true
			return m, nil
		}
		if msg.kind == "message" && msg.row < len(m.rows) {
			m.rows[msg.row].message = strings.TrimRight(msg.body, "\n")
			m.status = "message edited"
			m.statusErr = false
		}
		if msg.kind == "comment" && msg.row < len(m.rows) {
			m.rows[msg.row].comment = strings.TrimRight(msg.body, "\n")
			if m.rows[msg.row].comment == "" {
				m.status = "comment cleared"
			} else {
				m.status = "comment saved (will be sent to Claude)"
			}
			m.statusErr = false
		}
		return m, nil
	case reviewDiffLoadedMsg:
		key := msg.key
		if msg.err != nil {
			m.diffCache[key] = fmt.Sprintf("(could not load diff: %v)", msg.err)
		} else {
			m.diffCache[key] = msg.diff
		}
		if key == m.currentKey {
			m.diff.SetContent(colorizeDiff(m.diffCache[key], m.styles))
			m.diff.GotoTop()
		}
		return m, nil
	}
	return m, nil
}

func (m ReviewModel) handleKey(msg tea.KeyMsg) (ReviewModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Cancel):
		m.cancelled = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Confirm):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Focus):
		if m.focus == 0 {
			m.focus = 1
		} else {
			m.focus = 0
		}
		return m, nil
	case key.Matches(msg, m.keys.Up):
		if m.focus == 1 {
			m.diff.ScrollUp(1)
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
	case key.Matches(msg, m.keys.Down):
		if m.focus == 1 {
			m.diff.ScrollDown(1)
			return m, nil
		}
		if m.cursor < len(m.rows)-1 {
			m.cursor++
		}
	case key.Matches(msg, m.keys.PageUp):
		if m.focus == 1 {
			m.diff.HalfPageUp()
			return m, nil
		}
	case key.Matches(msg, m.keys.PageDown):
		if m.focus == 1 {
			m.diff.HalfPageDown()
			return m, nil
		}
	case key.Matches(msg, m.keys.Top):
		if m.focus == 1 {
			m.diff.GotoTop()
			return m, nil
		}
		m.cursor = 0
	case key.Matches(msg, m.keys.Bottom):
		if m.focus == 1 {
			m.diff.GotoBottom()
			return m, nil
		}
		if len(m.rows) > 0 {
			m.cursor = len(m.rows) - 1
		}
	case key.Matches(msg, m.keys.Reword):
		if len(m.rows) > 0 {
			return m, reviewEditorCmd(m.cursor, "message", m.rows[m.cursor].message)
		}
	case key.Matches(msg, m.keys.Comment):
		if len(m.rows) > 0 {
			template := m.commentTemplate(m.cursor)
			return m, reviewEditorCmd(m.cursor, "comment", template)
		}
	case key.Matches(msg, m.keys.Squash):
		if len(m.rows) > 0 {
			m = m.applySquash()
		}
	case key.Matches(msg, m.keys.Drop):
		if len(m.rows) > 0 {
			m = m.applyDrop()
		}
	}
	return m, m.maybeLoadDiff()
}

// reviewDiffLoadedMsg is dispatched when the async diff load completes.
type reviewDiffLoadedMsg struct {
	key  string
	diff string
	err  error
}

// diffKey computes the cache key for the current cursor row. Hunks are
// folded in so two groups that touch the same files but different hunks
// each cache their own slice of the diff.
func (m ReviewModel) diffKey() string {
	if len(m.rows) == 0 {
		return ""
	}
	r := m.rows[m.cursor]
	pool := m.pools[r.poolIdx]
	files := slices.Sorted(slices.Values(r.files))
	hunks := slices.Sorted(slices.Values(r.hunks))
	hunkParts := make([]string, len(hunks))
	for i, h := range hunks {
		hunkParts[i] = fmt.Sprintf("%d", h)
	}
	return pool.SHA + "\x00f:" + strings.Join(files, "\x00") + "\x00h:" + strings.Join(hunkParts, ",")
}

// maybeLoadDiff returns a Cmd that fetches the diff for the current row if
// not yet cached. If cached, it just sets the viewport content. If no
// loadDiff is wired, this is a no-op.
func (m *ReviewModel) maybeLoadDiff() tea.Cmd {
	if m.loadDiff == nil || len(m.rows) == 0 {
		return nil
	}
	key := m.diffKey()
	if key == "" {
		return nil
	}
	m.currentKey = key
	if cached, ok := m.diffCache[key]; ok {
		m.diff.SetContent(colorizeDiff(cached, m.styles))
		return nil
	}
	m.diff.SetContent(m.styles.subjectMuted.Render("(loading diff…)"))
	r := m.rows[m.cursor]
	pool := m.pools[r.poolIdx]
	files := append([]string(nil), r.files...)
	hunks := append([]int(nil), r.hunks...)
	loadDiff := m.loadDiff
	return func() tea.Msg {
		out, err := loadDiff(pool.SHA, pool.PoolSize, files)
		if err == nil && len(hunks) > 0 {
			out = sliceDiffByHunks(out, hunks)
		}
		return reviewDiffLoadedMsg{key: key, diff: out, err: err}
	}
}

// sliceDiffByHunks parses diff, picks only the indexed hunks, and re-emits a
// valid patch. Used so the review pane shows just the lines that belong to
// the current proposed commit when the group has line-level scope.
func sliceDiffByHunks(diff string, indices []int) string {
	hs, err := git.ParseHunks(diff)
	if err != nil || len(hs) == 0 {
		return diff
	}
	picked := make([]git.Hunk, 0, len(indices))
	for _, i := range indices {
		if i < 0 || i >= len(hs) {
			continue
		}
		picked = append(picked, hs[i])
	}
	return git.BuildPatch(picked)
}

func (m ReviewModel) commentTemplate(i int) string {
	r := m.rows[i]
	pool := m.pools[r.poolIdx]
	var b strings.Builder
	fmt.Fprintf(&b, "# Comment on proposed commit %d/%d of pool %s\n",
		i+1, len(m.rows), short(pool.SHA))
	fmt.Fprintf(&b, "# Files: %s\n", strings.Join(r.files, ", "))
	fmt.Fprintf(&b, "# Current message: %s\n", r.message)
	fmt.Fprintln(&b, "#")
	fmt.Fprintln(&b, "# Write your note to Claude below. Examples:")
	fmt.Fprintln(&b, "#   make this commit only about styling, move logic to next group")
	fmt.Fprintln(&b, "#   merge this with the next group")
	fmt.Fprintln(&b, "#   give this a more conventional-commits subject")
	fmt.Fprintln(&b, "# Empty / all-comments = clears any existing comment.")
	fmt.Fprintln(&b)
	if r.comment != "" {
		fmt.Fprintln(&b, r.comment)
	}
	return b.String()
}

func (m ReviewModel) applySquash() ReviewModel {
	r := &m.rows[m.cursor]
	if r.dropped || r.squashed {
		m.status = "already merged"
		m.statusErr = true
		return m
	}
	// Squash requires a previous active row in the same pool.
	if !m.hasActivePrev(m.cursor) {
		m.status = "no previous group in this pool to squash into"
		m.statusErr = true
		return m
	}
	r.squashed = true
	m.status = "squashed into previous group in pool"
	m.statusErr = false
	return m
}

func (m ReviewModel) applyDrop() ReviewModel {
	r := &m.rows[m.cursor]
	if r.dropped || r.squashed {
		m.status = "already merged"
		m.statusErr = true
		return m
	}
	if !m.hasActivePrev(m.cursor) {
		m.status = "no previous group in this pool to fold files into"
		m.statusErr = true
		return m
	}
	r.dropped = true
	m.status = "files folded into previous group; this message dropped"
	m.statusErr = false
	return m
}

func (m ReviewModel) hasActivePrev(i int) bool {
	pool := m.rows[i].poolIdx
	for j := i - 1; j >= 0; j-- {
		if m.rows[j].poolIdx != pool {
			return false
		}
		if !m.rows[j].dropped && !m.rows[j].squashed {
			return true
		}
	}
	return false
}

// reviewRewordMsg is delivered after the external $EDITOR closes.
type reviewRewordMsg struct {
	row  int
	kind string // "message" or "comment"
	body string
	err  error
}

func reviewEditorCmd(row int, kind, initial string) tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	tmp, err := os.CreateTemp("", "commit-composer-review-*.txt")
	if err != nil {
		return func() tea.Msg { return reviewRewordMsg{row: row, kind: kind, err: err} }
	}
	path := tmp.Name()
	if _, err := tmp.WriteString(initial); err != nil {
		tmp.Close()
		os.Remove(path)
		return func() tea.Msg { return reviewRewordMsg{row: row, kind: kind, err: err} }
	}
	tmp.Close()
	cmd := exec.Command(editor, path)
	return tea.ExecProcess(cmd, func(execErr error) tea.Msg {
		defer os.Remove(path)
		if execErr != nil {
			return reviewRewordMsg{row: row, kind: kind, err: execErr}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return reviewRewordMsg{row: row, kind: kind, err: err}
		}
		// Strip leading '#' comment lines for comment kind so the template
		// header doesn't end up in the actual comment.
		body := string(data)
		if kind == "comment" {
			body = stripCommentLines(body)
		}
		return reviewRewordMsg{row: row, kind: kind, body: body}
	})
}

func stripCommentLines(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		out = append(out, line)
	}
	joined := strings.Join(out, "\n")
	return strings.TrimSpace(joined)
}

// View implements tea.Model.
func (m ReviewModel) View() tea.View {
	return newTerminalView(m.renderView())
}

func (m ReviewModel) renderView() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}
	leftW := m.width * 45 / 100
	if leftW < 30 {
		leftW = 30
	}
	rightW := m.width - leftW
	bodyH := m.height - 3 // status bar + help line + status line
	if bodyH < 6 {
		bodyH = 6
	}
	left := m.renderProposalList(leftW, bodyH)
	right := m.renderRowDetail(rightW, bodyH)
	leftStyle := m.styles.pane
	rightStyle := m.styles.pane
	if m.focus == 0 {
		leftStyle = m.styles.paneFocused
	} else {
		rightStyle = m.styles.paneFocused
	}
	leftPane := leftStyle.Width(leftW).Height(bodyH).Render(left)
	rightPane := rightStyle.Width(rightW).Height(bodyH).Render(right)
	body := joinH(leftPane, rightPane)
	statusBar := m.renderReviewStatusBar()
	return joinV(body, statusBar, m.renderReviewFooter())
}

// renderReviewStatusBar mirrors Model.renderStatusBar for the review TUI.
func (m ReviewModel) renderReviewStatusBar() string {
	if len(m.rows) == 0 {
		return m.styles.statusBar.Width(m.width).Render("")
	}
	r := m.rows[m.cursor]
	pool := m.pools[r.poolIdx]
	segs := []string{
		"pool " + short(pool.SHA),
		fmt.Sprintf("%d/%d", m.cursor+1, len(m.rows)),
		fmt.Sprintf("%d files", len(r.files)),
		"state: " + rowState(r),
	}
	left := strings.Join(segs, "  │  ")
	right := "? help"
	if m.status != "" {
		right = m.status
	}
	leftR := m.styles.statusBar.Render(" " + left + " ")
	rightR := m.styles.statusBar.Render(" " + right + " ")
	gap := m.width - widthOf(leftR) - widthOf(rightR)
	if gap < 1 {
		gap = 1
	}
	mid := m.styles.statusBar.Render(strings.Repeat(" ", gap))
	return leftR + mid + rightR
}

func (m ReviewModel) renderProposalList(width, height int) string {
	if len(m.rows) == 0 {
		return m.styles.subjectMuted.Render("(no proposed commits)")
	}
	var b strings.Builder
	b.WriteString(m.styles.title.Render(
		fmt.Sprintf("Proposed commits (%d/%d)", m.cursor+1, len(m.rows))))
	b.WriteByte('\n')

	innerW := width - 2
	if innerW < 10 {
		innerW = 10
	}
	curPool := -1
	for i, r := range m.rows {
		if r.poolIdx != curPool {
			curPool = r.poolIdx
			pool := m.pools[curPool]
			// Distinct band: bold accent header on its own line + dim rule.
			header := fmt.Sprintf("  pool %s  ·  %d → %d",
				short(pool.SHA), pool.PoolSize, m.activeGroupsInPool(curPool))
			b.WriteByte('\n')
			b.WriteString(m.styles.title.Render(header))
			b.WriteByte('\n')
			b.WriteString(m.styles.subjectMuted.Render(strings.Repeat("─", innerW)))
			b.WriteByte('\n')
		}
		b.WriteString(m.renderReviewRow(r, i == m.cursor, innerW))
		b.WriteByte('\n')
	}
	return b.String()
}

// reviewTag returns the single-letter status glyph + a render-only filler
// space so the column is uniformly 3 chars wide (glyph + padding).
func (m ReviewModel) reviewTag(r reviewRow) string {
	switch {
	case r.dropped:
		return m.styles.tagDrop.Render(" D ")
	case r.squashed:
		return m.styles.tagSquash.Render(" S ")
	case r.comment != "":
		return m.styles.tagReword.Render(" N ")
	case r.message != r.origMessage:
		return m.styles.tagReword.Render(" R ")
	default:
		return m.styles.tagPick.Render(" · ")
	}
}

func (m ReviewModel) renderReviewRow(r reviewRow, selected bool, width int) string {
	cur := "  "
	if selected {
		cur = m.styles.cursor.Render("▶ ")
	}
	tag := m.reviewTag(r)
	// Layout: cursor(2) + tag(3) + " "(1) + subject(rest)
	subjBudget := width - 2 - 3 - 1
	if subjBudget < 4 {
		subjBudget = 4
	}
	subj := truncate(firstLine(r.message), subjBudget)
	if selected {
		// Full-width highlight: pad subj to fill remaining width so the
		// background extends across the row.
		padded := subj + strings.Repeat(" ", subjBudget-lipgloss.Width(subj))
		return cur + tag + " " + m.styles.rowSelected.Render(padded)
	}
	subjStyle := m.styles.subject
	if r.dropped || r.squashed {
		subjStyle = m.styles.subjectMuted
	}
	return cur + tag + " " + subjStyle.Render(subj)
}

// firstLine returns the first non-empty line of s so multiline messages
// don't blow up the row.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return s
}

func (m ReviewModel) activeGroupsInPool(p int) int {
	var n int
	for _, r := range m.rows {
		if r.poolIdx != p {
			continue
		}
		if !r.dropped && !r.squashed {
			n++
		}
	}
	return n
}

func (m ReviewModel) renderRowDetail(width, height int) string {
	if len(m.rows) == 0 {
		return ""
	}
	r := m.rows[m.cursor]
	pool := m.pools[r.poolIdx]
	var hdr strings.Builder
	hdr.WriteString(m.styles.title.Render("Proposed commit") + "\n")
	hdr.WriteString(m.styles.metaKey.Render("Pool:   ") + m.styles.meta.Render(
		fmt.Sprintf("%s (%d originals → %d proposed)", short(pool.SHA), pool.PoolSize, m.activeGroupsInPool(r.poolIdx))) + "\n")
	hdr.WriteString(m.styles.metaKey.Render("State:  ") + m.styles.meta.Render(rowState(r)) + "\n\n")

	hdr.WriteString(m.styles.title.Render("Message:") + "\n")
	hdr.WriteString(indent(r.message, "  ") + "\n\n")

	hdr.WriteString(m.styles.title.Render(fmt.Sprintf("Files (%d):", len(r.files))) + "\n")
	for _, f := range r.files {
		hdr.WriteString("  " + f + "\n")
	}
	if r.comment != "" {
		hdr.WriteString("\n" + m.styles.title.Render("Comment for Claude:") + "\n")
		hdr.WriteString(indent(r.comment, "  ") + "\n")
	}
	hdr.WriteString("\n")
	hdr.WriteString(m.styles.title.Render("Diff:") + "\n")
	header := hdr.String()
	headerLines := strings.Count(header, "\n")

	// Pane content area minus borders = height - 2.
	paneInner := height - 2
	diffH := paneInner - headerLines
	if diffH < 3 {
		diffH = 3
	}
	m.diff.SetHeight(diffH)
	diffW := width - 4
	if diffW < 10 {
		diffW = 10
	}
	m.diff.SetWidth(diffW)
	return header + m.diff.View()
}

func rowState(r reviewRow) string {
	switch {
	case r.dropped:
		return "dropped (files folded into previous group, message discarded)"
	case r.squashed:
		return "squashed (files + message folded into previous group)"
	case r.comment != "":
		return "note attached (Claude will revise after apply)"
	case r.message != r.origMessage:
		return "reworded"
	default:
		return "keep as proposed"
	}
}

func (m ReviewModel) renderReviewFooter() string {
	hk := m.styles.helpKey
	help := m.styles.help
	focusHint := "list"
	if m.focus == 1 {
		focusHint = "diff"
	}
	left := strings.Join([]string{
		hk.Render("tab") + " " + help.Render("focus("+focusHint+")"),
		hk.Render("j/k") + " " + help.Render("scroll"),
		hk.Render("r") + " " + help.Render("reword"),
		hk.Render("m") + " " + help.Render("comment"),
		hk.Render("s") + " " + help.Render("squash"),
		hk.Render("d") + " " + help.Render("drop"),
		hk.Render("⏎") + " " + help.Render("submit"),
		hk.Render("q") + " " + help.Render("cancel"),
	}, "  ")
	if m.status != "" {
		st := m.styles.status.Render(m.status)
		if m.statusErr {
			st = m.styles.statusError.Render(m.status)
		}
		gap := m.width - widthOf(left) - widthOf(st)
		if gap < 1 {
			gap = 1
		}
		return left + strings.Repeat(" ", gap) + st
	}
	return left
}

func widthOf(s string) int {
	// Cheap approximation - the actual ANSI-aware width is via lipgloss but
	// we only need it for footer spacing.
	return len(stripAnsi(s))
}

func stripAnsi(s string) string {
	var b strings.Builder
	in := false
	for _, r := range s {
		if r == 0x1b {
			in = true
			continue
		}
		if in {
			if r == 'm' {
				in = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func joinH(a, b string) string {
	la := strings.Split(a, "\n")
	lb := strings.Split(b, "\n")
	n := len(la)
	if len(lb) > n {
		n = len(lb)
	}
	var out []string
	for i := 0; i < n; i++ {
		var l, r string
		if i < len(la) {
			l = la[i]
		}
		if i < len(lb) {
			r = lb[i]
		}
		out = append(out, l+r)
	}
	return strings.Join(out, "\n")
}

func joinV(parts ...string) string { return strings.Join(parts, "\n") }

// short abbreviates a SHA for display.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Reference git to keep the import alive even if exec helpers reorganize.
var _ = git.Commit{}
