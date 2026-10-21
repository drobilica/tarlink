package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type detailAction struct {
	id                 contextualActionID
	label, description string
}

const actionPackage contextualActionID = actionHelp + 1

func (m model) detailPanel() bool {
	return m.screen == screenDetails && m.detail != nil && m.updatePlan == nil && len(m.selectedIDs) == 0
}

func (m model) detailActions() []detailAction {
	if m.detail == nil {
		return nil
	}
	value := m.detail
	var actions []detailAction
	if value.InstalledVersion == "" {
		actions = append(actions, detailAction{actionEnter, "Install", "Install the available release. Installation paths are checked before proceeding."})
	} else {
		if value.UpdateAvailable && !value.Pinned {
			actions = append(actions, detailAction{actionEnter, "Review update", fmt.Sprintf("Review the change from %s to %s before applying.", value.InstalledVersion, emptyDash(value.RegistryVersion))})
		}
		actions = append(actions, detailAction{actionVersions, "View versions", "Inspect version history and retained packages without changing the installation."})
		if value.PreviousVersion != "" {
			actions = append(actions, detailAction{actionRollback, "Rollback", "Restore the retained previous version, " + value.PreviousVersion + ". Confirmation required."})
		}
	}
	actions = append(actions, detailAction{actionPackage, "Package information", "Show package ID, categories, and homepage below the controls. Esc closes this information."})
	if value.InstalledVersion != "" {
		actions = append(actions, detailAction{actionUninstall, "Uninstall", "Remove the application and its managed integrations. Confirmation required; application data stays user-managed."})
	}
	return actions
}

func (m model) detailCursor() int {
	return min(max(0, m.detailSelected), max(0, len(m.detailActions())-1))
}

// detailLines keeps identity, versions, and the action heading at the same
// rows across installation states. Optional information follows the controls.
func (m model) detailLines(lines []string) []string {
	width := max(1, viewWidth(m.width)-2)
	value := *m.detail
	status := strings.ToUpper(applicationStatus(value))
	statusStyle := m.theme.muted
	if value.InstalledVersion != "" {
		statusStyle = m.theme.success
	}
	if value.Pinned || value.UpdateAvailable && value.InstalledVersion != "" {
		statusStyle = m.theme.warning
	}
	nameWidth := max(1, width-lipgloss.Width(status)-1)
	lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(nameWidth).Render(m.theme.panel.Render(truncate(detailName(&value), nameWidth, "…"))),
		lipgloss.NewStyle().PaddingLeft(1).Render(statusStyle.Render(status))),
		m.theme.muted.Render(truncate(value.Summary, width, "…")), "")
	channel := value.InstalledChannel
	if channel == "" {
		channel = value.DefaultChannel
	}
	labels := []string{"INSTALLED", "LATEST", "CHANNEL"}
	versions := []string{emptyDash(value.InstalledVersion), emptyDash(value.RegistryVersion), emptyDash(channel)}
	if value.InstalledVersion == "" {
		labels[1] = "AVAILABLE"
	}
	if value.UpdateAvailable && !value.Pinned && value.InstalledVersion != "" {
		versions[1] = m.theme.warning.Render(versions[1] + " ↑")
	}
	var headings, cells []string
	for i := range labels {
		cellWidth := width / 3
		if i == 2 {
			cellWidth = width - 2*(width/3)
		}
		style := lipgloss.NewStyle().Width(cellWidth)
		headings = append(headings, style.Render(m.theme.muted.Render(truncate(labels[i], cellWidth, "…"))))
		cells = append(cells, style.Render(truncate(versions[i], cellWidth, "…")))
	}
	lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, headings...), lipgloss.JoinHorizontal(lipgloss.Top, cells...), "", m.theme.panel.Render("ACTIONS"))
	actions := m.detailActions()
	cursor := m.detailCursor()
	rows := make([]table.Row, len(actions))
	for i, action := range actions {
		marker := " "
		if i == cursor {
			marker = "›"
		}
		rows[i] = table.Row{marker + " " + action.label}
	}
	menu := table.New(table.WithColumns([]table.Column{{Title: "", Width: width}}), table.WithRows(rows), table.WithFocused(true), table.WithHeight(len(rows)+1))
	styles := table.DefaultStyles()
	styles.Header = lipgloss.NewStyle()
	styles.Cell = lipgloss.NewStyle()
	styles.Selected = m.theme.selected
	menu.SetStyles(styles)
	menu.SetWidth(width)
	menu.SetCursor(cursor)
	// The menu has no column headings; Bubbles owns row rendering and selection.
	menuLines := strings.Split(menu.View(), "\n")
	lines = append(lines, menuLines[1:]...)
	title := "SELECTED ACTION"
	descriptionStyle := lipgloss.NewStyle()
	descriptionText := actions[cursor].description
	feedback := m.feedbackAppID == "" || m.feedbackAppID == value.ID
	if feedback && m.status != "" {
		title = "RESULT"
		descriptionText = m.status
		descriptionStyle = m.theme.success
		if m.feedbackPartial {
			descriptionStyle = m.theme.warning
		}
	}
	if feedback && m.err != nil {
		title = "OPERATION FAILED"
		descriptionText = m.err.Error()
		descriptionStyle = m.theme.danger
	}
	lines = append(lines, "", m.theme.muted.Render(title))
	description := wrappedLines(descriptionText, width)
	for i := 0; i < 2; i++ {
		line := ""
		if i < len(description) {
			line = description[i]
		}
		if i == 1 && len(description) > 2 {
			line = truncate(line, max(1, width-1), "") + "…"
		}
		lines = append(lines, descriptionStyle.Render(line))
	}
	if hasGameData(value) {
		lines = append(lines, "", truncate("Requires original game data · User-managed", width, "…"))
	}
	// A result temporarily occupies the fixed description area. Long feedback
	// remains available in full below; selecting an action restores its help.
	if feedback && (len(description) > 2 || m.err != nil && m.status != "") {
		lines = append(lines, "", m.theme.muted.Render("RESULT DETAILS"))
		if m.status != "" {
			lines = append(lines, wrappedLines(m.status, width)...)
		}
		if m.err != nil {
			lines = append(lines, wrappedLines(m.err.Error(), width)...)
		}
	}
	if m.packageExpanded {
		lines = append(lines, "")
		addPackageFields(&lines, value, width, m.theme)
	}
	return lines
}

func (m *model) moveDetailOrReview(delta int) {
	if !m.detailPanel() {
		m.moveReviewScroll(delta)
		return
	}
	m.detailSelected = (m.detailCursor() + delta + len(m.detailActions())) % len(m.detailActions())
	m.ensureDetailSelection()
}

func (m *model) ensureDetailSelection() {
	view := m.reviewViewport(m.listTableBudget())
	// Breadcrumb (2), identity (3), versions (3), and action heading (1).
	view.EnsureVisible(9+m.detailCursor(), 0, 1)
	m.reviewScroll = view.YOffset()
}

func (m model) chooseDetailAction() (tea.Model, tea.Cmd) {
	action := m.detailActions()[m.detailCursor()]
	switch action.id {
	case actionPackage:
		m.packageExpanded = !m.packageExpanded
		if m.packageExpanded {
			view := m.reviewViewport(m.listTableBudget())
			view.EnsureVisible(len(m.reviewLines())-1, 0, 1)
			m.reviewScroll = view.YOffset()
		}
		return m, nil
	case actionEnter:
		if m.detail.InstalledVersion == "" && len(m.detail.ChannelHeads) > 1 {
			m.channels = channelNames(m.detail)
			m.channelSelected = channelIndex(m.channels, m.detail.DefaultChannel)
			m.screen = screenInstallChannel
			return m, nil
		}
		m.channels = nil
		m.channelSelected = 0
		return m.activateSelected()
	default:
		// Dispatch through the existing action policy and confirmation workflows.
		for _, candidate := range m.contextualActionPolicy() {
			if candidate.id == action.id {
				return m.updateKey(tea.KeyPressMsg{Text: candidate.binding.Keys()[0]})
			}
		}
	}
	return m, nil
}
