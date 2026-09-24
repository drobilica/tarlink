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
	panel, control, controlSelected, modal            lipgloss.Style
}

func newTheme(color bool) tuiTheme {
	styles := tuiTheme{modal: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2)}
	if !color {
		return styles
	}
	styles.accent = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	styles.success = lipgloss.NewStyle().Foreground(lipgloss.Color("32"))
	styles.warning = lipgloss.NewStyle().Foreground(lipgloss.Color("33"))
	styles.danger = lipgloss.NewStyle().Foreground(lipgloss.Color("31"))
	styles.muted = lipgloss.NewStyle().Foreground(lipgloss.Color("90"))
	styles.selected = lipgloss.NewStyle().Reverse(true).Bold(true)
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
	Cancel, Quit, CtrlC, Help                                keypkg.Binding
}

type contextualActionID uint8

const (
	actionUp contextualActionID = iota
	actionDown
	actionFilter
	actionToggle
	actionEnter
	actionSearch
	actionInstalled
	actionUpdates
	actionUpgrade
	actionVersions
	actionRollback
	actionUninstall
	actionRemoveConflict
	actionCancel
	actionQuit
	actionHelp
)

type contextualAction struct {
	id      contextualActionID
	binding keypkg.Binding
	label   string
}

func newKeyMap() tuiKeyMap {
	return tuiKeyMap{
		Up:        keypkg.NewBinding(keypkg.WithKeys("up", "k"), keypkg.WithHelp("↑/k", "Navigate")),
		Down:      keypkg.NewBinding(keypkg.WithKeys("down", "j"), keypkg.WithHelp("↓/j", "Navigate")),
		Left:      keypkg.NewBinding(keypkg.WithKeys("left", "h"), keypkg.WithHelp("←/h", "Filter")),
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
		Help:      keypkg.NewBinding(keypkg.WithKeys("?"), keypkg.WithHelp("?", "Help")),
	}
}

// contextualActionPolicy is the sole source of truth for actions exposed by
// the footer and accepted by the keyboard. Busy and search states are
// intentionally exclusive so background operations cannot receive shortcuts.
func (m model) contextualActionPolicy() []contextualAction {
	actions := m.contextualActions()
	if m.busy == "" && !m.searching {
		actions = append(actions, contextualAction{id: actionHelp, binding: newKeyMap().Help, label: "? Help"})
	}
	return actions
}

func (m model) contextualActions() []contextualAction {
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
	if m.screen == screenUninstall && m.uninstallConflict != nil {
		remove := keypkg.NewBinding(keypkg.WithKeys("d"), keypkg.WithHelp("d", "Remove conflicting file"))
		return []contextualAction{action(actionRemoveConflict, remove, "d Remove conflicting file"), action(actionCancel, b.Cancel, "Esc Cancel"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenUninstallConflictConfirm {
		label := "Enter Confirm"
		if m.screen == screenUpgrade {
			label = "Enter Upgrade"
		} else if m.screen == screenInstallConfirm {
			label = "Enter Install anyway"
		} else if m.screen == screenUninstallConflictConfirm {
			label = "Enter Remove file"
		}
		actions := []contextualAction{action(actionEnter, b.Enter, label), action(actionCancel, b.Cancel, "Esc Cancel"), action(actionQuit, b.Quit, "q Quit")}
		if m.modalScrollable() {
			actions = append([]contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate")}, actions...)
		}
		return actions
	}
	if m.screen == screenInstallChannel {
		move := keypkg.NewBinding(keypkg.WithKeys("up", "down"))
		return []contextualAction{action(actionUp, move, "↑/↓ Choose"), action(actionEnter, b.Enter, "Enter Select"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.screen == screenDetails {
		enterLabel := "Enter Apply"
		if len(m.selectedIDs) > 0 {
			if m.returnTo == screenAvailable {
				enterLabel = "Enter Install selected"
			}
			if m.returnTo == screenInstalled {
				enterLabel = "Enter Uninstall selected"
			}
		} else if m.detail != nil {
			switch {
			case m.detail.InstalledVersion == "":
				enterLabel = "Enter Install"
			case m.detail.Pinned:
				enterLabel = "Enter Pinned status"
			case m.detail.UpdateAvailable:
				enterLabel = "Enter Update"
			default:
				enterLabel = "Enter Check status"
			}
		}
		actions := []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionEnter, b.Enter, enterLabel)}
		if m.detail != nil && m.detail.InstalledVersion != "" {
			actions = append(actions, action(actionVersions, b.Versions, "v Versions"), action(actionRollback, b.Rollback, "r Rollback"), action(actionUninstall, b.Uninstall, "x Uninstall"))
		}
		return append(actions, action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit"))
	}
	if m.screen == screenVersions {
		return []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionRollback, b.Rollback, "r Rollback"), action(actionUninstall, b.Uninstall, "x Uninstall"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
	}
	if m.isListScreen() {
		enterLabel := "Enter Details"
		if len(m.selectedIDs) > 0 {
			enterLabel = "Enter Review"
		}
		actions := make([]contextualAction, 0)
		if len(m.visibleApplications()) > 0 {
			actions = append(actions, action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionEnter, b.Enter, enterLabel))
		}
		if len(m.visibleApplications()) > 0 && (m.screen == screenAvailable || m.screen == screenInstalled) {
			toggle := keypkg.NewBinding(keypkg.WithKeys("space"), keypkg.WithHelp("Space", "Select"))
			actions = append(actions, action(actionToggle, toggle, "Space Toggle"))
		}
		if m.screen == screenAvailable {
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
		if m.selectedInstalled() {
			actions = append(actions, action(actionVersions, b.Versions, "v Versions"))
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
	helper := m.help
	if helper.ShortSeparator == "" {
		helper = newHelp(m.color)
	}
	bindings := m.actionBindings()
	// Short help gives the primary action and help discovery first. The full
	// reference retains every eligible binding in the same policy order.
	if len(bindings) > 0 {
		prioritized := make([]keypkg.Binding, 0, len(bindings))
		for _, want := range []string{"↑↓", "Enter", "Space", "?", "Esc", "/", "q"} {
			for _, binding := range bindings {
				if binding.Help().Key == want {
					prioritized = append(prioritized, binding)
				}
			}
		}
		bindings = prioritized
	}
	helper.SetWidth(max(1, viewWidth(m.width)))
	return helper.ShortHelpView(bindings)
}

// actionBindings keeps every action legend, including modal legends, tied to
// the contextual action policy used by updateKey.
func (m model) actionBindings() []keypkg.Binding {
	bindings := make([]keypkg.Binding, 0, len(m.contextualActionPolicy()))
	seen := make(map[string]bool)
	for _, action := range m.contextualActionPolicy() {
		if seen[action.label] {
			continue
		}
		seen[action.label] = true
		binding := action.binding
		if action.label == "Navigate" {
			binding = keypkg.NewBinding(keypkg.WithKeys("up", "down", "k", "j"), keypkg.WithHelp("↑↓", "Navigate"))
		}
		binding.SetHelp(binding.Help().Key, strings.TrimPrefix(action.label, binding.Help().Key+" "))
		bindings = append(bindings, binding)
	}
	// Help is always a discoverable shell action, including while a modal is
	// active. The keyboard handler uses the same binding for input dispatch.
	return bindings
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
