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
	Up, Down, Left, Right, Tabs, Enter, Search keypkg.Binding
	Browse, Installed, NotInstalled, Updates   keypkg.Binding
	Upgrade, Versions, Rollback, Uninstall     keypkg.Binding
	Cancel, Quit, CtrlC, Help                  keypkg.Binding
}

type contextualActionID uint8

const (
	actionUp contextualActionID = iota
	actionDown
	actionToggle
	actionEnter
	actionSearch
	actionTabs
	actionBrowse
	actionInstalled
	actionNotInstalled
	actionUpdates
	actionUpgrade
	actionVersions
	actionRollback
	actionUninstall
	actionRemoveConflict
	actionCancel
	actionQuit
	actionScroll
	actionHelp
)

type contextualAction struct {
	id      contextualActionID
	binding keypkg.Binding
	label   string
}

func newKeyMap() tuiKeyMap {
	return tuiKeyMap{
		Up:           keypkg.NewBinding(keypkg.WithKeys("up", "k"), keypkg.WithHelp("↑/k", "Navigate")),
		Down:         keypkg.NewBinding(keypkg.WithKeys("down", "j"), keypkg.WithHelp("↓/j", "Navigate")),
		Left:         keypkg.NewBinding(keypkg.WithKeys("left")),
		Right:        keypkg.NewBinding(keypkg.WithKeys("right")),
		Tabs:         keypkg.NewBinding(keypkg.WithKeys("left", "right"), keypkg.WithHelp("←/→", "Switch tab")),
		Enter:        keypkg.NewBinding(keypkg.WithKeys("enter"), keypkg.WithHelp("Enter", "Details")),
		Search:       keypkg.NewBinding(keypkg.WithKeys("/"), keypkg.WithHelp("/", "Search")),
		Browse:       keypkg.NewBinding(keypkg.WithKeys("b", "B"), keypkg.WithHelp("b", "Browse")),
		Installed:    keypkg.NewBinding(keypkg.WithKeys("i", "I"), keypkg.WithHelp("i", "Installed")),
		NotInstalled: keypkg.NewBinding(keypkg.WithKeys("n", "N"), keypkg.WithHelp("n", "Not installed")),
		Updates:      keypkg.NewBinding(keypkg.WithKeys("u", "U"), keypkg.WithHelp("u", "Updates")),
		Upgrade:      keypkg.NewBinding(keypkg.WithKeys("g"), keypkg.WithHelp("g", "Upgrade TarLink")),
		Versions:     keypkg.NewBinding(keypkg.WithKeys("v"), keypkg.WithHelp("v", "Versions")),
		Rollback:     keypkg.NewBinding(keypkg.WithKeys("r"), keypkg.WithHelp("r", "Rollback")),
		Uninstall:    keypkg.NewBinding(keypkg.WithKeys("x", "d", "delete"), keypkg.WithHelp("x", "Uninstall")),
		Cancel:       keypkg.NewBinding(keypkg.WithKeys("esc"), keypkg.WithHelp("Esc", "Back")),
		Quit:         keypkg.NewBinding(keypkg.WithKeys("q"), keypkg.WithHelp("q", "Quit")),
		CtrlC:        keypkg.NewBinding(keypkg.WithKeys("ctrl+c")),
		Help:         keypkg.NewBinding(keypkg.WithKeys("?"), keypkg.WithHelp("?", "Help")),
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
	if m.dockScrollable() && !m.searching {
		scroll := keypkg.NewBinding(keypkg.WithKeys("pgup", "pgdown"), keypkg.WithHelp("PgUp/PgDn", "Result"))
		actions = append([]contextualAction{{id: actionScroll, binding: scroll, label: "PgUp/PgDn Result"}}, actions...)
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
		if m.updatePlan != nil {
			return []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionEnter, b.Enter, "Enter Confirm updates"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
		}
		if len(m.selectedIDs) > 0 && m.batchFinished {
			scroll := keypkg.NewBinding(keypkg.WithKeys("pgup", "pgdown"), keypkg.WithHelp("PgUp/PgDn", "Scroll"))
			return []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionScroll, scroll, "PgUp/PgDn Scroll"), action(actionCancel, b.Cancel, "Esc Back"), action(actionQuit, b.Quit, "q Quit")}
		}
		if len(m.selectedIDs) > 0 {
			if m.returnTo == screenAvailable || m.returnTo == screenNotInstalled {
				enterLabel = "Enter Install selected"
			}
			if m.returnTo == screenInstalled {
				enterLabel = "Enter Uninstall selected"
			}
		} else if m.detailPanel() {
			enterLabel = "Enter Choose"
		}
		actions := []contextualAction{action(actionUp, b.Up, "Navigate"), action(actionDown, b.Down, "Navigate"), action(actionEnter, b.Enter, enterLabel)}
		if m.detail != nil && m.detail.InstalledVersion != "" {
			actions = append(actions, action(actionVersions, b.Versions, "v Versions"), action(actionUninstall, b.Uninstall, "x Uninstall"))
			if m.detail.PreviousVersion != "" {
				actions = append(actions, action(actionRollback, b.Rollback, "r Rollback"))
			}
		}
		if m.detailPanel() {
			scroll := keypkg.NewBinding(keypkg.WithKeys("pgup", "pgdown"), keypkg.WithHelp("PgUp/PgDn", "Scroll"))
			actions = append(actions, action(actionScroll, scroll, "PgUp/PgDn Scroll"))
		}
		actions = append(actions, action(actionBrowse, b.Browse, "b Browse"), action(actionInstalled, b.Installed, "i Installed"), action(actionNotInstalled, b.NotInstalled, "n Not installed"), action(actionUpdates, b.Updates, "u Updates"))
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
		// The list footer is a fixed shell legend: navigation, tabs, Enter,
		// selection, search, help, and quit stay in place on every list tab,
		// including empty ones. Space selection is omitted on Updates, where
		// batch operations are unsupported.
		actions := []contextualAction{
			action(actionUp, b.Up, "Navigate"),
			action(actionDown, b.Down, "Navigate"),
			action(actionTabs, b.Tabs, "←/→ Tabs"),
			action(actionEnter, b.Enter, enterLabel),
		}
		if m.screen != screenUpdates {
			toggle := keypkg.NewBinding(keypkg.WithKeys("space"), keypkg.WithHelp("Space", "Select"))
			actions = append(actions, action(actionToggle, toggle, "Space Toggle"))
		}
		actions = append(actions, action(actionSearch, b.Search, "/ Search"))
		if m.screen != screenAvailable {
			actions = append(actions, action(actionBrowse, b.Browse, "b Browse"))
		}
		if m.screen != screenInstalled {
			actions = append(actions, action(actionInstalled, b.Installed, "i Installed"))
		}
		if m.screen != screenNotInstalled {
			actions = append(actions, action(actionNotInstalled, b.NotInstalled, "n Not installed"))
		}
		if m.screen != screenUpdates {
			actions = append(actions, action(actionUpdates, b.Updates, "u Updates"))
		}
		if m.screen != screenAvailable {
			actions = append(actions, action(actionCancel, b.Cancel, "Esc Browse"))
		}
		if m.selectedInstalled() {
			actions = append(actions, action(actionVersions, b.Versions, "v Versions"))
		}
		if m.upgradeAvailable {
			actions = append([]contextualAction{action(actionUpgrade, b.Upgrade, "g Upgrade")}, actions...)
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
	// Long result scrolling is the primary action while that result is visible.
	if m.dockScrollable() {
		helper.SetWidth(max(1, viewWidth(m.width)))
		return helper.ShortHelpView(bindings)
	}
	// List screens share one fixed shell legend so switching tabs or
	// emptying a list never changes the footer. Up, Down, and Enter stay in
	// the legend even with no rows, matching the keyboard policy. Help
	// discovery and quit remain at the end.
	if m.isListScreen() {
		bindings = prioritizeListBindings(bindings)
	}
	if m.detailPanel() {
		bindings = detailFooterBindings(bindings)
	}
	helper.SetWidth(max(1, viewWidth(m.width)))
	return helper.ShortHelpView(bindings)
}

func prioritizeListBindings(bindings []keypkg.Binding) []keypkg.Binding {
	if len(bindings) == 0 {
		return bindings
	}
	prioritized := make([]keypkg.Binding, 0, len(bindings))
	for _, want := range []string{"↑↓", "←/→", "Enter", "Space", "?", "/", "q"} {
		for _, binding := range bindings {
			if binding.Help().Key == want {
				prioritized = append(prioritized, binding)
			}
		}
	}
	return prioritized
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
		if m.detailPanel() && action.label == "Navigate" {
			binding.SetHelp("↑↓", "Select")
		}
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

func detailFooterBindings(bindings []keypkg.Binding) []keypkg.Binding {
	var result []keypkg.Binding
	for _, want := range []string{"↑↓", "Enter", "Esc", "?", "q", "v", "x", "r"} {
		for _, binding := range bindings {
			if binding.Help().Key == want {
				result = append(result, binding)
			}
		}
	}
	return result
}
