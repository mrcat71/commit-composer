package tui

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mrcat71/commit-composer/internal/git"
	"github.com/mrcat71/commit-composer/internal/plan"
)

const (
	minLeftPaneWidth = 28
	leftPaneRatio    = 0.40 // 40% of width for the commit list
)

// View implements tea.Model.
func (m Model) View() tea.View {
	return newTerminalView(m.renderView())
}

func newTerminalView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) renderView() string {
	if m.width == 0 || m.height == 0 {
		return "loading…"
	}
	left, right := m.paneWidths()
	leftBody := m.renderList(left)
	rightBody := m.renderDetails(right)

	leftStyle := m.styles.pane
	rightStyle := m.styles.pane
	if m.focus == 0 {
		leftStyle = m.styles.paneFocused
	} else {
		rightStyle = m.styles.paneFocused
	}

	leftPane := leftStyle.Width(left).Height(m.bodyHeight()).Render(leftBody)
	rightPane := rightStyle.Width(right).Height(m.bodyHeight()).Render(rightBody)
	body := lipgloss.JoinHorizontal(lipgloss.Top, leftPane, rightPane)
	statusBar := m.renderStatusBar()
	footer := m.renderFooter()
	out := lipgloss.JoinVertical(lipgloss.Left, body, statusBar, footer)
	if m.showHelp {
		return overlay(out, m.renderHelp(), m.width, m.height)
	}
	return out
}

// renderStatusBar produces the segmented full-width status bar shown between
// the body and the help line. Mirrors revdiff's layout: identity on the
// left, status hint on the right, all on a tinted background.
func (m Model) renderStatusBar() string {
	if len(m.rows) == 0 {
		return m.styles.statusBar.Width(m.width).Render("")
	}
	r := m.rows[m.cursor]
	segs := []string{
		"⎇ " + r.commit.Short,
		fmt.Sprintf("%d/%d", m.cursor+1, len(m.rows)),
		fmt.Sprintf("range %s", m.rangeSpec),
	}
	if r.action != plan.Pick {
		segs = append(segs, "action: "+actionLabel(r.action))
	}
	left := strings.Join(segs, "  │  ")
	right := "? help"
	if m.status != "" {
		right = m.status
	}
	return m.statusBarRow(left, right)
}

func (m Model) statusBarRow(left, right string) string {
	leftR := m.styles.statusBar.Render(" " + left + " ")
	rightR := m.styles.statusBar.Render(" " + right + " ")
	gap := m.width - lipgloss.Width(leftR) - lipgloss.Width(rightR)
	if gap < 1 {
		gap = 1
	}
	mid := m.styles.statusBar.Render(strings.Repeat(" ", gap))
	return leftR + mid + rightR
}

func actionLabel(a plan.Action) string {
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
		return "recompose"
	}
	return ""
}

func (m Model) paneWidths() (int, int) {
	if m.width == 0 {
		return 40, 40
	}
	left := int(float64(m.width) * leftPaneRatio)
	if left < minLeftPaneWidth {
		left = minLeftPaneWidth
	}
	if left > m.width-30 {
		left = m.width - 30
	}
	right := m.width - left
	if right < 20 {
		right = 20
	}
	return left, right
}

func (m Model) bodyHeight() int {
	h := m.height - 3 // reserve 1 line for status bar + 2 for help/status
	if h < 4 {
		h = 4
	}
	return h
}

// listViewportHeight returns the number of commit rows that fit in the left
// pane given the current window height.
//
// Pane height = bodyHeight. Pane has 2 lines of borders (top + bottom), so
// the inner content area is bodyHeight - 2. Inside that area we reserve:
//   - 1 line for the title
//   - up to 1 line for the "↓ N more" scroll-down indicator
//
// (The "↑ N more" indicator goes on the title line, so it doesn't cost an
// extra line.)
//
// Worst case content = 1 + rows + 1 = rows + 2 lines.
// We need rows + 2 <= bodyHeight - 2, so rows <= bodyHeight - 4.
func (m Model) listViewportHeight() int {
	h := m.bodyHeight() - 4
	if h < 1 {
		h = 1
	}
	return h
}

func (m Model) renderList(width int) string {
	if len(m.rows) == 0 {
		return m.styles.subjectMuted.Render("(no commits in range)")
	}
	first, last := m.listWindow()

	var b strings.Builder
	title := m.styles.title.Render(fmt.Sprintf("Commits (%d/%d, newest first)", m.cursor+1, len(m.rows)))
	b.WriteString(title)
	if first > 0 {
		b.WriteString(m.styles.help.Render(fmt.Sprintf("  ↑ %d more", first)))
	}
	b.WriteByte('\n')

	for i := first; i < last; i++ {
		line := m.renderRow(m.rows[i], i == m.cursor, width-2)
		b.WriteString(line)
		b.WriteByte('\n')
	}

	if last < len(m.rows) {
		b.WriteString(m.styles.help.Render(fmt.Sprintf("  ↓ %d more", len(m.rows)-last)))
		b.WriteByte('\n')
	}
	return b.String()
}

// listWindow returns the [first, last) row indices visible in the left pane,
// always inclusive of the cursor. Robust to listOffset getting stale after
// reorders, drops, or window resizes.
func (m Model) listWindow() (int, int) {
	total := len(m.rows)
	avail := m.listViewportHeight()
	if avail >= total {
		return 0, total
	}
	// Clamp listOffset into a valid range, then ensure cursor is visible.
	first := m.listOffset
	if first < 0 {
		first = 0
	}
	if first > total-avail {
		first = total - avail
	}
	if m.cursor < first {
		first = m.cursor
	}
	if m.cursor >= first+avail {
		first = m.cursor - avail + 1
	}
	if first < 0 {
		first = 0
	}
	last := first + avail
	if last > total {
		last = total
	}
	return first, last
}

const (
	actionTagWidth = 8 // width budget for the action tag area (6-char label + 2 padding)
	dateColWidth   = 6 // width budget for the relative-date column
)

func (m Model) renderRow(r row, selected bool, width int) string {
	cursor := "  "
	if selected {
		cursor = m.styles.cursor.Render("> ")
	}
	tag := m.tagFor(r.action)
	short := m.styles.short.Render(padOrTrunc(r.commit.Short, 7))
	var dateStr string
	if r.commit.SHA == git.UncommittedSHA {
		dateStr = "now"
	} else {
		dateStr = relativeDate(r.commit.Date, time.Now())
	}
	date := m.styles.short.Render(padOrTrunc(dateStr, dateColWidth))
	subjStyle := m.styles.subject
	if r.action == plan.Drop {
		subjStyle = m.styles.subjectMuted
	}
	if r.commit.SHA == git.UncommittedSHA {
		subjStyle = m.styles.tagRecompose // make uncommitted stand out
	}
	if selected {
		subjStyle = m.styles.rowSelected
	}
	// Layout: cursor(2) + tag(8) + " "(1) + sha(7) + " "(1) + date(6) + " "(1) + subject
	subjBudget := width - 2 - actionTagWidth - 1 - 7 - 1 - dateColWidth - 1
	subject := truncate(r.commit.Subject, max(8, subjBudget))
	return cursor + tag + " " + short + " " + date + " " + subjStyle.Render(subject)
}

// relativeDate renders a compact 6-char date for the commit list:
//
//	just now        -> "now"
//	< 60 minutes    -> "<n>m"
//	< 24 hours      -> "<n>h"
//	< 7 days        -> "<n>d"
//	same year       -> "Jan 02"
//	older           -> "06-01-02" (YY-MM-DD)
func relativeDate(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case t.Year() == now.Year():
		return t.Format("Jan 02")
	default:
		return t.Format("06-01-02")
	}
}

// shortLabel maps an Action to the abbreviated label used in the commit-list
// tag. Width is uniform (6) so columns align without huge dead green space.
func shortLabel(a plan.Action) string {
	switch a {
	case plan.Pick:
		return "pick  "
	case plan.Reword:
		return "reword"
	case plan.Squash:
		return "squash"
	case plan.Fixup:
		return "fixup "
	case plan.Drop:
		return "drop  "
	case plan.Edit:
		return "edit  "
	case plan.ClaudeRecompose:
		return "redo  "
	}
	return "?     "
}

func (m Model) tagFor(a plan.Action) string {
	label := shortLabel(a)
	switch a {
	case plan.Pick:
		return m.styles.tagPick.Render(label)
	case plan.Reword:
		return m.styles.tagReword.Render(label)
	case plan.Squash:
		return m.styles.tagSquash.Render(label)
	case plan.Fixup:
		return m.styles.tagFixup.Render(label)
	case plan.Drop:
		return m.styles.tagDrop.Render(label)
	case plan.Edit:
		return m.styles.tagEdit.Render(label)
	case plan.ClaudeRecompose:
		return m.styles.tagRecompose.Render(label)
	}
	return m.styles.tag.Render(label)
}

func (m Model) renderDetails(width int) string {
	if len(m.rows) == 0 {
		return ""
	}
	r := m.rows[m.cursor]

	// Build the header section (everything above the diff viewport) into a
	// separate buffer so we can measure its line count and size the diff
	// viewport to whatever's left in the pane content area.
	var hdr strings.Builder
	hdr.WriteString(m.styles.title.Render("Commit "+r.commit.Short) + "\n")
	hdr.WriteString(m.styles.metaKey.Render("SHA:    ") + m.styles.meta.Render(r.commit.SHA) + "\n")
	hdr.WriteString(m.styles.metaKey.Render("Author: ") + m.styles.meta.Render(r.commit.Author+" <"+r.commit.Email+">") + "\n")
	hdr.WriteString(m.styles.metaKey.Render("Date:   ") + m.styles.meta.Render(r.commit.Date.Format("2006-01-02 15:04:05 -0700")) + "\n")
	if r.action == plan.ClaudeRecompose {
		first, last := m.poolBounds(m.cursor)
		hdr.WriteString(m.styles.metaKey.Render("Pool:   ") +
			m.styles.tagRecompose.Render(fmt.Sprintf(" %d commits ", last-first)) +
			m.styles.meta.Render("   (consecutive recompose marks pool together)") + "\n")
	}
	if r.action == plan.Reword && r.reword != "" {
		hdr.WriteString("\n")
		hdr.WriteString(m.styles.title.Render("New message:") + "\n")
		hdr.WriteString(indent(r.reword, "  ") + "\n")
	} else {
		hdr.WriteString("\n")
		hdr.WriteString(m.styles.title.Render("Message:") + "\n")
		hdr.WriteString(indent(r.commit.Message(), "  ") + "\n")
	}
	if files, ok := m.filesCache[r.commit.SHA]; ok {
		hdr.WriteString("\n")
		hdr.WriteString(m.styles.title.Render(fmt.Sprintf("Files (%d):", len(files))) + "\n")
		hdr.WriteString(renderFileTree(files, m.styles) + "\n")
	} else if m.loadFiles != nil {
		// File list not loaded yet — show a placeholder. The async load
		// is kicked off from Update on cursor-move; we never shell out to
		// git from View(), since View runs synchronously on every render
		// (including for every mouse-motion event).
		hdr.WriteString("\n")
		hdr.WriteString(m.styles.help.Render("Files: loading…") + "\n")
	}
	hdr.WriteString("\n")
	headerStr := hdr.String()

	// Measure header height (in screen lines, counting wraps would be
	// closer-to-perfect but a line count is sufficient when our content
	// rarely wraps).
	headerLines := strings.Count(headerStr, "\n")

	// Pane content area = paneHeight - 2 (top + bottom borders).
	// Diff viewport gets whatever remains.
	paneInner := m.bodyHeight() - 2
	diffH := paneInner - headerLines
	if diffH < 3 {
		diffH = 3
	}
	// Mutate the viewport size for this render. The receiver is a value, so
	// this only affects the display copy.
	m.diff.SetHeight(diffH)
	diffW := width - 4
	if diffW < 10 {
		diffW = 10
	}
	m.diff.SetWidth(diffW)

	return headerStr + m.diff.View()
}

// renderFileTree renders the file list as a directory-grouped tree.
//
// Files are grouped by their parent directory; common parents are collapsed
// so a single shared dir prints once on its own line, with the leaf names
// indented under it. The status letter (M / A / D / ...) is shown as a
// colored prefix on each leaf.
//
// Example for [M dot_claude/settings.json, M dot_zshrc, A private_dot_local/private_bin/exec]:
//
//	  M dot_zshrc
//	dot_claude/
//	  M settings.json
//	private_dot_local/private_bin/
//	  A exec
func renderFileTree(files []git.FileStat, s styles) string {
	if len(files) == 0 {
		return s.subjectMuted.Render("  (none)")
	}
	// Group by directory prefix.
	groups := make(map[string][]git.FileStat)
	var dirs []string
	rootless := []git.FileStat{} // files at repo root
	for _, f := range files {
		dir, _ := splitDir(f.Path)
		if dir == "" {
			rootless = append(rootless, f)
			continue
		}
		if _, ok := groups[dir]; !ok {
			dirs = append(dirs, dir)
		}
		groups[dir] = append(groups[dir], f)
	}
	slices.Sort(dirs)

	var b strings.Builder
	// Root-level files first.
	for _, f := range rootless {
		b.WriteString("  ")
		b.WriteString(statusBadge(f.Status, s))
		b.WriteByte(' ')
		b.WriteString(filepath.Base(f.Path))
		b.WriteByte('\n')
	}
	for _, d := range dirs {
		b.WriteString(s.metaKey.Render(d + "/"))
		b.WriteByte('\n')
		for _, f := range groups[d] {
			b.WriteString("  ")
			b.WriteString(statusBadge(f.Status, s))
			b.WriteByte(' ')
			b.WriteString(filepath.Base(f.Path))
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitDir returns the parent dir and basename of p. Mirrors filepath.Split
// but trims the trailing slash so the dir is a clean prefix.
func splitDir(p string) (dir, base string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// statusBadge colors the M/A/D/R/T/U marker.
func statusBadge(status string, s styles) string {
	switch {
	case strings.HasPrefix(status, "A"):
		return s.diffAdd.Render(status)
	case strings.HasPrefix(status, "D"):
		return s.diffDel.Render(status)
	case strings.HasPrefix(status, "R"), strings.HasPrefix(status, "C"):
		return s.diffHunk.Render(status)
	default:
		return s.meta.Render(status)
	}
}

func (m Model) renderFooter() string {
	left := m.helpLine()
	right := m.statusLine()
	if right == "" {
		return left
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) helpLine() string {
	focusedHint := "commits"
	if m.focus == 1 {
		focusedHint = "diff"
	}
	pairs := []struct{ k, v string }{
		{"[c]", "mark for recompose"},
		{"j/k", "scroll " + focusedHint},
		{"tab", "switch pane"},
		{"J/K", "reorder"},
		{"⏎", "apply"},
		{"q", "cancel"},
		{"?", "more"},
	}
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("  ")
		}
		b.WriteString(m.styles.helpKey.Render(p.k))
		b.WriteByte(' ')
		b.WriteString(m.styles.help.Render(p.v))
	}
	return b.String()
}

func (m Model) statusLine() string {
	if m.status == "" {
		return ""
	}
	if m.statusError {
		return m.styles.statusError.Render(m.status)
	}
	return m.styles.status.Render(m.status)
}

func (m Model) renderHelp() string {
	title := "commit-composer"
	hk := m.styles.helpKey
	mut := m.styles.subjectMuted
	lines := []string{
		m.styles.title.Render("PRIMARY FLOW"),
		"",
		hk.Render("c") + "         mark this commit for claude-recompose",
		mut.Render("          Mark several in a row -> they get pooled. Claude looks at"),
		mut.Render("          the combined diff and proposes a fresh set of commits"),
		mut.Render("          (could be more, fewer, or the same number - it decides"),
		mut.Render("          by feature). You review and can comment to refine."),
		"",
		hk.Render("j / k  or  ↓ / ↑") + "   move cursor (or scroll diff when right pane focused)",
		hk.Render("tab / h / l") + "       switch focus between left and right pane",
		mut.Render("                       focused pane has a highlighted border"),
		hk.Render("g / G") + "             first / last commit",
		hk.Render("ctrl+u / ctrl+d") + "   page up / down (commits when left, diff when right)",
		hk.Render("J / K") + "             reorder: move highlighted commit down / up",
		hk.Render("ctrl+j / ctrl+k") + "   scroll the diff (always, regardless of focus)",
		"",
		hk.Render("⏎") + "         confirm plan and exit (apply happens after chat review)",
		hk.Render("q / esc") + "   cancel without changes",
		"",
		m.styles.title.Render("ADVANCED  (per-commit rebase actions, optional)"),
		"",
		hk.Render("r") + "         reword: edit just the message (opens $EDITOR)",
		hk.Render("s") + "         squash: fold into previous commit, keep both messages",
		hk.Render("f") + "         fixup:  fold into previous commit, drop this message",
		hk.Render("d") + "         drop:   discard this commit entirely",
		hk.Render("e") + "         edit:   pause rebase here for manual amend",
		hk.Render("space") + "     cycle through all action states",
		"",
		mut.Render("Default for every commit is 'pick' (keep as-is)."),
		mut.Render("You only need to learn 'c' for the primary flow."),
	}
	body := strings.Join(lines, "\n")
	return m.styles.modal.Render(m.styles.modalTitle.Render(title) + "\n\n" + body)
}

// overlay paints `top` centered on top of `base`.
func overlay(base, top string, w, h int) string {
	bg := strings.Split(base, "\n")
	fg := strings.Split(top, "\n")
	topH := len(fg)
	topW := 0
	for _, l := range fg {
		if lw := lipgloss.Width(l); lw > topW {
			topW = lw
		}
	}
	startRow := (h - topH) / 2
	startCol := (w - topW) / 2
	if startRow < 0 {
		startRow = 0
	}
	if startCol < 0 {
		startCol = 0
	}
	for i, line := range fg {
		row := startRow + i
		if row < 0 || row >= len(bg) {
			continue
		}
		// Replace the chunk at startCol..startCol+topW in bg[row] with the
		// overlay line. lipgloss handles ANSI widths well enough for our
		// purposes; for the help modal, just clobber.
		bg[row] = pasteAt(bg[row], line, startCol)
	}
	return strings.Join(bg, "\n")
}

// pasteAt overwrites `dst` starting at visible column `col` with `src`.
// dst is expected to contain ANSI escape sequences (styled lipgloss output);
// we slice it by visible width via ansi.Truncate / ansi.TruncateLeft so we
// never cut in the middle of a `\x1b[...m` sequence. Cutting mid-sequence
// would leave garbled fragments like `;2;213;137;95m` rendered as literal
// text on the screen.
//
// Resets (`\x1b[0m`) bracket `src` so any style left open by the left chunk
// or carried forward into the right chunk doesn't bleed into the overlay.
func pasteAt(dst, src string, col int) string {
	const reset = "\x1b[0m"
	dw := lipgloss.Width(dst)
	if col >= dw {
		return dst + reset + strings.Repeat(" ", col-dw) + src + reset
	}
	sw := lipgloss.Width(src)
	left := ansi.Truncate(dst, col, "")
	if col+sw >= dw {
		return left + reset + src + reset
	}
	right := ansi.TruncateLeft(dst, col+sw, "")
	return left + reset + src + reset + right
}

// colorizeDiff applies foreground colors to a unified diff for the right
// pane and prefixes every body line with a line-number gutter sourced from
// the @@ hunk headers (revdiff style).
//
//	  12 │ context
//	+ 13 │ +added line          (new-file line number)
//	- 12 │ -removed line        (old-file line number)
//	... ┊ @@ -a,b +c,d @@       (file header / hunk header)
//
// We intentionally use lipgloss styles directly rather than chroma so the
// dependency surface stays small.
func colorizeDiff(d string, s styles) string {
	const gutterW = 5
	var b strings.Builder
	oldLine, newLine := 0, 0
	for _, line := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "diff "):
			b.WriteString(s.gutter.Render(padLeft("", gutterW) + " │ "))
			b.WriteString(s.diffFile.Render(line))
		case strings.HasPrefix(line, "@@"):
			o, n := parseHunkHeader(line)
			if o > 0 {
				oldLine = o
			}
			if n > 0 {
				newLine = n
			}
			b.WriteString(s.gutter.Render(padLeft("…", gutterW) + " │ "))
			b.WriteString(s.diffHunk.Render(line))
		case strings.HasPrefix(line, "+"):
			b.WriteString(s.gutter.Render(padLeft(gutterNum(newLine), gutterW) + " │ "))
			b.WriteString(s.diffAdd.Render(line))
			newLine++
		case strings.HasPrefix(line, "-"):
			b.WriteString(s.gutter.Render(padLeft(gutterNum(oldLine), gutterW) + " │ "))
			b.WriteString(s.diffDel.Render(line))
			oldLine++
		case line == "":
			b.WriteByte('\n')
			continue
		default:
			b.WriteString(s.gutter.Render(padLeft(gutterNum(newLine), gutterW) + " │ "))
			b.WriteString(s.diffContext.Render(line))
			oldLine++
			newLine++
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// parseHunkHeader pulls the starting old and new line numbers out of a unified
// diff hunk header `@@ -OLD[,n] +NEW[,m] @@`. Returns 0 on parse error so the
// caller keeps the previous counters.
func parseHunkHeader(s string) (oldStart, newStart int) {
	// minimal hand parser - avoids pulling in regexp for one call site.
	// Locate the '-' that follows "@@ ".
	i := strings.Index(s, "-")
	if i < 0 {
		return 0, 0
	}
	rest := s[i+1:]
	// old range
	end := indexAny2(rest, ",", " ")
	if end < 0 {
		return 0, 0
	}
	oldStart = atoi(rest[:end])
	// find +
	j := strings.Index(rest, "+")
	if j < 0 {
		return oldStart, 0
	}
	rest2 := rest[j+1:]
	end2 := indexAny2(rest2, ",", " ")
	if end2 < 0 {
		return oldStart, 0
	}
	newStart = atoi(rest2[:end2])
	return oldStart, newStart
}

func indexAny2(s, a, b string) int {
	ia := strings.Index(s, a)
	ib := strings.Index(s, b)
	switch {
	case ia < 0:
		return ib
	case ib < 0:
		return ia
	case ia < ib:
		return ia
	default:
		return ib
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func gutterNum(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%d", n)
}

func padLeft(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return strings.Repeat(" ", n-lipgloss.Width(s)) + s
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len([]rune(s)) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string([]rune(s)[:n-1]) + "…"
}

func padOrTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) == n {
		return s
	}
	if len(r) > n {
		return string(r[:n])
	}
	return s + strings.Repeat(" ", n-len(r))
}

func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
