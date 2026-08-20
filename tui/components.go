package tui

import (
	"charm.land/bubbles/v2/help"
	keypkg "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/lipgloss/v2"
)

type tuiTheme struct {
	accent, success, warning, danger, muted, selected lipgloss.Style
}

func newTheme(color bool) tuiTheme {
	styles := tuiTheme{}
	if !color {
		return styles
	}
	styles.accent = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	styles.success = lipgloss.NewStyle().Foreground(lipgloss.Color("32"))
	styles.warning = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	styles.danger = lipgloss.NewStyle().Foreground(lipgloss.Color("31"))
	styles.muted = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
	styles.selected = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("36")).Bold(true)
	return styles
}

func (t tuiTheme) render(value string, valueTone tone) string {
	styles := [...]lipgloss.Style{t.accent, t.success, t.warning, t.danger, t.muted}
	return styles[valueTone].Render(value)
}

type tuiKeyMap struct {
	Up, Down, Left, Right, Enter, Search, Installed, Updates keypkg.Binding
	Upgrade, Versions, Rollback, Uninstall                   keypkg.Binding
	Cancel, Quit, CtrlC                                      keypkg.Binding
}

func newKeyMap() tuiKeyMap {
	return tuiKeyMap{
		Up:        keypkg.NewBinding(keypkg.WithKeys("up"), keypkg.WithHelp("↑", "Navigate")),
		Down:      keypkg.NewBinding(keypkg.WithKeys("down"), keypkg.WithHelp("↓", "Navigate")),
		Left:      keypkg.NewBinding(keypkg.WithKeys("left"), keypkg.WithHelp("←/→", "Filter")),
		Right:     keypkg.NewBinding(keypkg.WithKeys("right")),
		Enter:     keypkg.NewBinding(keypkg.WithKeys("enter"), keypkg.WithHelp("Enter", "Details")),
		Search:    keypkg.NewBinding(keypkg.WithKeys("/"), keypkg.WithHelp("/", "Search")),
		Installed: keypkg.NewBinding(keypkg.WithKeys("i"), keypkg.WithHelp("i", "Installed")),
		Updates:   keypkg.NewBinding(keypkg.WithKeys("u"), keypkg.WithHelp("u", "Updates")),
		Upgrade:   keypkg.NewBinding(keypkg.WithKeys("U"), keypkg.WithHelp("U", "Upgrade TarLink")),
		Versions:  keypkg.NewBinding(keypkg.WithKeys("v"), keypkg.WithHelp("v", "Versions")),
		Rollback:  keypkg.NewBinding(keypkg.WithKeys("r"), keypkg.WithHelp("r", "Rollback")),
		Uninstall: keypkg.NewBinding(keypkg.WithKeys("x", "d", "delete"), keypkg.WithHelp("x", "Uninstall")),
		Cancel:    keypkg.NewBinding(keypkg.WithKeys("esc"), keypkg.WithHelp("Esc", "Back")),
		Quit:      keypkg.NewBinding(keypkg.WithKeys("q"), keypkg.WithHelp("q", "Quit")),
		CtrlC:     keypkg.NewBinding(keypkg.WithKeys("ctrl+c")),
	}
}

func (m model) keyMap() tuiKeyMap {
	b := newKeyMap()
	if m.busy != "" {
		b.Up.SetEnabled(false)
		b.Down.SetEnabled(false)
		b.Left.SetEnabled(false)
		b.Right.SetEnabled(false)
		b.Enter.SetEnabled(false)
		b.Search.SetEnabled(false)
		b.Installed.SetEnabled(false)
		b.Updates.SetEnabled(false)
		b.Upgrade.SetEnabled(false)
		b.Versions.SetEnabled(false)
		b.Rollback.SetEnabled(false)
		b.Uninstall.SetEnabled(false)
	}
	if !m.upgradeAvailable {
		b.Upgrade.SetEnabled(false)
	}
	return b
}

func newHelp(color bool) help.Model {
	h := help.New()
	h.ShortSeparator = "  "
	h.Styles = help.Styles{}
	if color {
		h.Styles.ShortKey = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
		h.Styles.ShortDesc = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
		h.Styles.ShortSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
	}
	return h
}

func (m model) helpView() string {
	h := m.help
	if h.ShortSeparator == "" {
		h.ShortSeparator = "  "
	}
	h.SetWidth(viewWidth(m.width))
	b := m.keyMap()
	if m.busy != "" {
		// During an active operation Esc cancels (not "back"); reuse the same
		// Cancel key binding, just relabeled, alongside q to quit.
		cancel := b.Cancel
		cancel.SetHelp("Esc", "Cancel")
		return h.ShortHelpView([]keypkg.Binding{cancel, b.Quit})
	}
	if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm {
		return h.ShortHelpView([]keypkg.Binding{b.Enter, b.Cancel, b.Quit})
	}
	if m.screen == screenDetails || m.screen == screenVersions {
		return h.ShortHelpView([]keypkg.Binding{b.Cancel, b.Quit})
	}
	if m.upgradeAvailable {
		bindings := []keypkg.Binding{b.Upgrade, b.Up, b.Down, b.Enter, b.Quit}
		if m.screen == screenAvailable {
			bindings = []keypkg.Binding{b.Upgrade, b.Up, b.Down, b.Left, b.Enter, b.Quit}
		}
		return h.ShortHelpView(bindings)
	}
	bindings := []keypkg.Binding{b.Up, b.Down, b.Enter, b.Search, b.Installed, b.Updates, b.Quit}
	if m.screen == screenAvailable {
		bindings = []keypkg.Binding{b.Up, b.Down, b.Left, b.Enter, b.Search, b.Installed, b.Updates, b.Quit}
	}
	return h.ShortHelpView(bindings)
}

func newProgress(color bool) progress.Model {
	bar := progress.New(progress.WithFillCharacters('█', '░'), progress.WithWidth(progressBarWidth))
	if color {
		bar.FullColor = lipgloss.Color("36")
		bar.EmptyColor = lipgloss.Color("90")
	} else {
		bar.FullColor = nil
		bar.EmptyColor = nil
	}
	return bar
}
