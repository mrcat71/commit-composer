package tui

import "github.com/charmbracelet/lipgloss"

// styles bundles all lipgloss styles used by the TUI. Centralized so theming
// changes are a single edit.
type styles struct {
	pane          lipgloss.Style
	paneFocused   lipgloss.Style
	title         lipgloss.Style
	cursor        lipgloss.Style
	row           lipgloss.Style
	rowSelected   lipgloss.Style
	tag           lipgloss.Style
	tagPick       lipgloss.Style
	tagReword     lipgloss.Style
	tagSquash     lipgloss.Style
	tagFixup      lipgloss.Style
	tagDrop       lipgloss.Style
	tagEdit       lipgloss.Style
	tagRecompose  lipgloss.Style
	short         lipgloss.Style
	subject       lipgloss.Style
	subjectMuted  lipgloss.Style
	help          lipgloss.Style
	helpKey       lipgloss.Style
	status        lipgloss.Style
	statusError   lipgloss.Style
	statusSuccess lipgloss.Style
	diffAdd       lipgloss.Style
	diffDel       lipgloss.Style
	diffHunk      lipgloss.Style
	diffFile      lipgloss.Style
	meta          lipgloss.Style
	metaKey       lipgloss.Style
	modal         lipgloss.Style
	modalTitle    lipgloss.Style
}

func newStyles() styles {
	border := lipgloss.RoundedBorder()
	return styles{
		pane: lipgloss.NewStyle().
			Border(border).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1),
		paneFocused: lipgloss.NewStyle().
			Border(border).
			BorderForeground(lipgloss.Color("212")).
			Padding(0, 1),
		title: lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true),
		cursor: lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true),
		row: lipgloss.NewStyle(),
		rowSelected: lipgloss.NewStyle().
			Background(lipgloss.Color("237")).
			Foreground(lipgloss.Color("231")).
			Bold(true),
		tag: lipgloss.NewStyle().
			Padding(0, 1).
			Bold(true),
		// pick is the default state - render subtle so commits look "unmarked"
		// until the user actively assigns a different action.
		tagPick: lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.Color("240")),
		tagReword: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("33")). // blue
			Foreground(lipgloss.Color("231")),
		tagSquash: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("166")). // orange
			Foreground(lipgloss.Color("231")),
		tagFixup: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("130")). // dim orange
			Foreground(lipgloss.Color("231")),
		tagDrop: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("124")). // red
			Foreground(lipgloss.Color("231")),
		tagEdit: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("99")). // purple
			Foreground(lipgloss.Color("231")),
		tagRecompose: lipgloss.NewStyle().
			Padding(0, 1).
			Background(lipgloss.Color("178")). // yellow
			Foreground(lipgloss.Color("16")).
			Bold(true),
		short: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
		subject: lipgloss.NewStyle(),
		subjectMuted: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
		help: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
		helpKey: lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true),
		status: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
		statusError: lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true),
		statusSuccess: lipgloss.NewStyle().
			Foreground(lipgloss.Color("46")).
			Bold(true),
		diffAdd: lipgloss.NewStyle().
			Foreground(lipgloss.Color("46")),
		diffDel: lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")),
		diffHunk: lipgloss.NewStyle().
			Foreground(lipgloss.Color("51")),
		diffFile: lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true),
		meta: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")),
		metaKey: lipgloss.NewStyle().
			Foreground(lipgloss.Color("250")).
			Bold(true),
		modal: lipgloss.NewStyle().
			Border(border).
			BorderForeground(lipgloss.Color("212")).
			Padding(1, 2),
		modalTitle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("212")).
			Bold(true),
	}
}
