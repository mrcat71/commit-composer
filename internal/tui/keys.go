package tui

import "github.com/charmbracelet/bubbles/key"

// keymap groups all keybindings so the help overlay can render them
// declaratively.
type keymap struct {
	Up         key.Binding
	Down       key.Binding
	Top        key.Binding
	Bottom     key.Binding
	MoveUp     key.Binding
	MoveDown   key.Binding
	Pick        key.Binding
	Reword      key.Binding
	Squash      key.Binding
	Fixup       key.Binding
	Drop        key.Binding
	Edit        key.Binding
	ClaudeRecompose key.Binding
	Cycle       key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding

	FocusToggle key.Binding
	FocusLeft   key.Binding
	FocusRight  key.Binding
	Confirm    key.Binding
	Cancel     key.Binding
	Help       key.Binding
}

func newKeymap() keymap {
	return keymap{
		Up: key.NewBinding(
			key.WithKeys("k", "up"),
			key.WithHelp("k/↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("j", "down"),
			key.WithHelp("j/↓", "down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("gg/Home", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G/End", "bottom"),
		),
		MoveUp: key.NewBinding(
			key.WithKeys("K", "shift+up"),
			key.WithHelp("K", "move up"),
		),
		MoveDown: key.NewBinding(
			key.WithKeys("J", "shift+down"),
			key.WithHelp("J", "move down"),
		),
		Pick:        key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pick")),
		Reword:      key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reword")),
		Squash:      key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "squash")),
		Fixup:       key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fixup")),
		Drop:        key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "drop")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		ClaudeRecompose: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "claude-recompose (pool consecutive marks)")),
		Cycle: key.NewBinding(
			key.WithKeys(" "),
			key.WithHelp("space", "cycle action"),
		),
		ScrollUp: key.NewBinding(
			key.WithKeys("ctrl+k"),
			key.WithHelp("ctrl+k", "diff up"),
		),
		ScrollDown: key.NewBinding(
			key.WithKeys("ctrl+j"),
			key.WithHelp("ctrl+j", "diff down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("ctrl+u", "pgup"),
			key.WithHelp("ctrl+u", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("ctrl+d", "pgdown"),
			key.WithHelp("ctrl+d", "page down"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "apply plan"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("q", "ctrl+c", "esc"),
			key.WithHelp("q/esc", "cancel"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),

		FocusToggle: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "switch focus"),
		),
		FocusLeft: key.NewBinding(
			key.WithKeys("h", "shift+tab", "left"),
			key.WithHelp("h", "focus commits"),
		),
		FocusRight: key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("l", "focus diff"),
		),
	}
}
