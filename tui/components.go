package tui

import (
	"strings"

	"charm.land/bubbles/v2/help"
	keypkg "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type tuiTheme struct {
	accent, success, warning, danger, muted, selected lipgloss.Style
	panel, control, controlSelected                   lipgloss.Style
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
	styles.selected = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
	styles.panel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("36"))
	styles.control = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
	styles.controlSelected = lipgloss.NewStyle().Foreground(lipgloss.Color("36")).Bold(true)
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

type contextualActionID uint8

const (
	actionUp contextualActionID = iota
	actionDown
	actionFilter
	actionEnter
	actionSearch
	actionInstalled
	actionUpdates
	actionUpgrade
	actionVersions
	actionRollback
	actionUninstall
	actionCancel
	actionQuit
)

type contextualAction struct {
	id      contextualActionID
	binding keypkg.Binding
	label   string
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

// contextualActionPolicy is the sole source of truth for actions exposed by
// the footer and accepted by the keyboard. Busy and search states are
// intentionally exclusive so background operations cannot receive shortcuts.
func (m model) contextualActionPolicy() []contextualAction {
	b := newKeyMap()
	action := func(id contextualActionID, binding keypkg.Binding, label string) contextualAction {
		return contextualAction{id: id, binding: binding, label: label}
	}
	if m.busy != "" {
		cancel := b.Cancel
		cancel.SetHelp("Esc", "Cancel")
		return []contextualAction{action(actionCancel, cancel, "Esc Cancel"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.searching {
		return []contextualAction{action(actionEnter, b.Enter, "Enter Search"), action(actionCancel, b.Cancel, "Esc Cancel")}
	}
	if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm {
		label := "Enter Confirm"
		if m.screen == screenUpgrade {
			label = "Enter Upgrade"
		} else if m.screen == screenInstallConfirm {
			label = "Enter Install anyway"
		}
		return []contextualAction{action(actionEnter, b.Enter, label), action(actionCancel, b.Cancel, "Esc Cancel"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.screen == screenInstallChannel {
		move := keypkg.NewBinding(keypkg.WithKeys("up", "down"))
		return []contextualAction{action(actionUp, move, "↑/↓ Choose"), action(actionEnter, b.Enter, "Enter Select"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.screen == screenDetails {
		actions := make([]contextualAction, 0, 6)
		if m.detail != nil {
			if m.detail.InstalledVersion == "" {
				actions = append(actions, action(actionEnter, b.Enter, "Enter Install"))
			} else {
				if !m.detail.Pinned && m.detail.UpdateAvailable {
					actions = append(actions, action(actionEnter, b.Enter, "Enter Update"))
				}
				actions = append(actions, action(actionVersions, b.Versions, "v Versions"), action(actionRollback, b.Rollback, "r Rollback"), action(actionUninstall, b.Uninstall, "x Uninstall"))
			}
		}
		actions = append(actions, action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit"))
		return actions
	}
	if m.screen == screenVersions {
		return []contextualAction{action(actionRollback, b.Rollback, "r Rollback"), action(actionUninstall, b.Uninstall, "x Uninstall"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.isListScreen() {
		actions := []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionEnter, b.Enter, "Open")}
		if len(m.selectedIDs) > 0 && m.screen == screenAvailable {
			toggle := keypkg.NewBinding(keypkg.WithKeys(" "))
			actions = append(actions, action(actionFilter, toggle, "Space Toggle"), action(actionInstalled, b.Installed, "i Install selected"))
		} else if len(m.selectedIDs) > 0 && m.screen == screenInstalled {
			toggle := keypkg.NewBinding(keypkg.WithKeys(" "))
			actions = append(actions, action(actionFilter, toggle, "Space Toggle"), action(actionUpdates, b.Updates, "u Uninstall selected"))
		} else if m.screen == screenAvailable {
			filter := keypkg.NewBinding(keypkg.WithKeys("left", "right"))
			actions = append(actions, action(actionFilter, filter, "←/→ Filter"), action(actionSearch, b.Search, "/ Search"), action(actionInstalled, b.Installed, "i Installed"), action(actionUpdates, b.Updates, "u Updates"))
		} else if m.screen == screenInstalled {
			actions = append(actions, action(actionSearch, b.Search, "/ Search"), action(actionUpdates, b.Updates, "u Updates"))
		} else {
			actions = append(actions, action(actionSearch, b.Search, "/ Search"), action(actionInstalled, b.Installed, "i Installed"))
		}
		if m.screen != screenAvailable {
			actions = append(actions, action(actionCancel, b.Cancel, "Esc Browse"))
		}
		if m.upgradeAvailable {
			actions = append([]contextualAction{action(actionUpgrade, b.Upgrade, "U Upgrade")}, actions...)
		}
		actions = append(actions, action(actionQuit, b.Quit, "q Quit"))
		return actions
	}
	return []contextualAction{action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
}

func (m model) matchesAction(message tea.KeyPressMsg, id contextualActionID) bool {
	for _, action := range m.contextualActionPolicy() {
		if action.id == id && keypkg.Matches(message, action.binding) {
			return true
		}
	}
	return false
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
	labels := make([]string, 0, len(m.contextualActionPolicy()))
	for _, action := range m.contextualActionPolicy() {
		if len(labels) == 0 || labels[len(labels)-1] != action.label {
			labels = append(labels, action.label)
		}
	}
	return strings.Join(labels, "  ")
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
