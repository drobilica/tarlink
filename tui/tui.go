// Package tui is TarLink's presentation-only terminal interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/help"
	keypkg "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/clipperhouse/displaywidth"
	"github.com/drobilica/tarlink/internal/app"
)

type screen uint8

const (
	screenAvailable screen = iota
	screenInstalled
	screenUpdates
	screenDetails
	screenVersions
	screenRollback
	screenUninstall
	screenUpgrade
	screenInstallConfirm
	screenInstallChannel
	screenUninstallConflictConfirm
)

type applicationFilter uint8

const (
	filterAll applicationFilter = iota
	filterInstalled
	filterNotInstalled
)

type loadedMsg struct {
	available []app.Application
	installed []app.Application
	err       error
}

type searchMsg struct {
	values    []app.Application
	err       error
	cancelled bool
}

type versionsMsg struct {
	values    []app.Version
	err       error
	cancelled bool
}
type versionMsg struct {
	value app.TarLinkVersion
	err   error
}

type pathCheckMsg struct {
	appID     string
	conflicts []app.PathConflict
	err       error
}

type batchResolveMsg struct {
	targets []app.BatchTarget
	err     error
}

type operationMsg struct {
	message      string
	err          error
	next         screen
	move         bool
	clearUpgrade bool
	conflict     *app.UninstallConflict
}

type progressMsg struct {
	hub   *operationHub
	event app.Progress
}

type operationHub struct {
	mu        sync.Mutex
	pending   []app.Progress
	latest    *app.Progress
	lastStage app.ProgressStage
	lastEmit  time.Time
	wake      chan struct{}
	result    chan operationMsg
	ctx       context.Context
}

type model struct {
	ctx                 context.Context
	service             app.Service
	screen              screen
	returnTo            screen
	versionsFromDetails bool
	confirmTo           screen
	confirmSet          bool
	pathConflicts       []app.PathConflict
	available           []app.Application
	installed           []app.Application
	applicationFilter   applicationFilter
	versions            []app.Version
	selected            int
	cursorID            string
	selectedIDs         map[string]bool
	batchIDs            []string
	batchTargets        []app.BatchTarget
	batchUninstall      bool
	channelSelected     int
	channels            []string
	detail              *app.Application
	searching           bool
	query               string
	busy                string
	status              string
	err                 error
	width               int
	height              int
	progress            app.Progress
	progressSpeed       float64
	progressSpeedAt     time.Time
	estimator           speedEstimator
	color               bool
	theme               tuiTheme
	help                help.Model
	applicationTable    table.Model
	progressBar         progress.Model
	tarlinkVersion      app.TarLinkVersion
	upgradeAvailable    bool
	cancel              context.CancelFunc
	opCancel            context.CancelFunc
	uninstallConflict   *app.UninstallConflict
	helpOverlay         bool
	componentsReady     bool
	searchInput         textinput.Model
}

// Run starts the terminal renderer. All application changes are delegated to
// the same service API used by the CLI.
func Run(ctx context.Context, service app.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return fmt.Errorf("TarLink core is unavailable")
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	m := model{ctx: operationContext, service: service, screen: screenAvailable, color: colorEnabled(output), theme: newTheme(colorEnabled(output)), help: newHelp(colorEnabled(output)), progressBar: newProgress(colorEnabled(output)), cancel: cancel}
	m.initComponents()
	program := tea.NewProgram(
		m,
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output),
	)
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(m.loadCmd(), m.checkVersionCmd()) }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	m.initComponents()
	if m.isListScreen() {
		m.configureApplicationTable(m.visibleApplications(), max(1, viewWidth(m.width)-4), max(3, m.height-8))
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampSelection()
		m.help.SetWidth(m.width)
		m.progressBar.SetWidth(progressBarWidthFor(m.width))
		m.searchInput.SetWidth(max(12, m.width-18))
		return m, nil
	case loadedMsg:
		m.busy = ""
		m.err = message.err
		if message.err == nil {
			m.available = message.available
			m.installed = message.installed
			m.refreshDetail()
			if m.detail == nil {
				values := m.visibleApplications()
				if len(values) > 0 {
					value := values[0]
					m.detail = &value
				}
			}
		}
		m.clampSelection()
		return m, nil
	case versionMsg:
		if message.err == nil {
			m.tarlinkVersion = message.value
			m.upgradeAvailable = message.value.UpgradeAvailable
		}
		return m, nil
	case pathCheckMsg:
		m.busy = ""
		if message.err != nil {
			m.err = message.err
			return m, nil
		}
		m.pathConflicts = message.conflicts
		if len(message.conflicts) != 0 {
			m.returnTo = m.screen
			m.screen = screenInstallConfirm
			return m, nil
		}
		return m.startInstall(m.installSelector(message.appID))
	case batchResolveMsg:
		m.busy = ""
		m.opCancel = nil
		if message.err != nil {
			m.err = message.err
			return m, nil
		}
		m.batchTargets = message.targets
		m.screen = screenDetails
		return m, nil
	case searchMsg:
		m.busy = ""
		m.opCancel = nil
		if message.cancelled {
			m.err = nil
			m.status = "Search cancelled"
			return m, nil
		}
		m.err = message.err
		if message.err == nil {
			m.available = message.values
		}
		m.selected = 0
		return m, nil
	case versionsMsg:
		m.busy = ""
		m.opCancel = nil
		if message.cancelled {
			m.err = nil
			m.status = "Loading cancelled"
			return m, nil
		}
		m.err = message.err
		if message.err == nil {
			m.versions = message.values
			m.screen = screenVersions
		}
		return m, nil
	case operationMsg:
		m.busy = ""
		m.resetProgress()
		m.opCancel = nil
		m.err = message.err
		m.uninstallConflict = message.conflict
		m.status = message.message
		if errors.Is(message.err, context.Canceled) {
			m.err = nil
			m.status = "Operation cancelled"
			m.batchIDs, m.batchTargets = nil, nil
			m.selectedIDs = nil
			return m, nil
		}
		if message.err != nil {
			m.status = ""
			return m, nil
		}
		if message.clearUpgrade {
			m.upgradeAvailable = false
		}
		if message.move {
			m.screen = message.next
			m.confirmSet = false
			if m.screen != screenDetails {
				m.detail = nil
			}
			m.batchIDs, m.batchTargets = nil, nil
			m.selectedIDs = nil
		}
		return m, m.loadCmd()
	case progressMsg:
		now := time.Now()
		counterReset := message.event.BytesDone < m.progress.BytesDone || (message.event.BytesDone == 0 && m.progress.BytesDone > 0)
		if m.progress.Stage != message.event.Stage || counterReset {
			m.estimator.Reset()
			m.progressSpeed = 0
			m.progressSpeedAt = time.Time{}
		}
		m.progress = message.event
		rate := m.estimator.Add(now, message.event.BytesDone)
		if rate > 0 && (m.progressSpeedAt.IsZero() || now.Sub(m.progressSpeedAt) >= 500*time.Millisecond) {
			m.progressSpeed = rate
			m.progressSpeedAt = now
		}
		return m, waitProgress(message.hub)
	case tea.KeyPressMsg:
		return m.updateKey(message)
	}
	return m, nil
}

func (m model) View() tea.View {
	m.initComponents()
	body := m.bodyLines()
	footer := fit(m.helpView(), m.width)
	if m.height > 0 {
		available := max(0, m.height-1)
		if len(body) > available {
			body = body[:available]
		}
		body = append(body, make([]string, max(0, available-len(body)))...)
	}
	lines := append(body, footer)
	view := tea.NewView(strings.Join(lines, "\n") + "\n")
	view.AltScreen = true
	return view
}

// bodyLines is the shared shell and screen content.
func (m model) bodyLines() []string {
	lines := []string{m.headerLine(), m.tabsLine(), m.separator()}
	add := func(value string) {
		for _, part := range strings.Split(value, "\n") {
			lines = append(lines, fit(part, m.width))
		}
	}
	if m.status != "" {
		add(m.style(m.status, success))
		lines = append(lines, "")
	}
	if m.err != nil {
		add(m.style("Operation failed", danger))
		add(m.style(m.err.Error(), danger))
		lines = append(lines, "")
	}
	if m.uninstallConflict != nil {
		add("Conflicting integration: " + m.uninstallConflict.Path)
	}
	if m.busy != "" {
		add(m.style(m.busy, accent))
		add(m.progressLine())
		lines = append(lines, "")
	}
	if m.upgradeAvailable {
		add(m.style("TarLink update available: "+m.tarlinkVersion.Current+" → "+m.tarlinkVersion.Latest, warning))
	}
	workspace, _ := m.workspaceLines()
	if m.isOverlay() || m.screen == screenDetails || m.screen == screenVersions {
		content := workspace
		if m.isOverlay() {
			content = m.overlayLines()
		}
		remaining := max(1, max(1, m.height)-len(lines)-1)
		card := m.renderCard(content, viewWidth(m.width))
		placed := lipgloss.Place(viewWidth(m.width), remaining, lipgloss.Center, lipgloss.Center, card)
		for _, line := range strings.Split(placed, "\n") {
			lines = append(lines, fit(line, m.width))
		}
	} else {
		for _, line := range workspace {
			lines = append(lines, fit(line, m.width))
		}
	}
	if m.helpOverlay {
		lines = append(lines, "")
		for _, line := range m.helpOverlayLines() {
			lines = append(lines, fit(line, m.width))
		}
	}
	return lines
}

func (m *model) initComponents() {
	if m.componentsReady {
		return
	}
	m.searchInput = textinput.New()
	m.searchInput.Prompt = ""
	m.searchInput.Placeholder = "Search applications"
	m.searchInput.CharLimit = 128
	m.applicationTable = table.New(table.WithFocused(true))
	styles := table.DefaultStyles()
	// Bubbles applies these styles outside each column's content width. Keep
	// the header and cells on the same geometry so their contents align.
	styles.Header = m.theme.panel.Padding(0, 1)
	styles.Cell = applicationTableCellStyle
	styles.Selected = m.theme.selected
	m.applicationTable.SetStyles(styles)
	m.componentsReady = true
}

var applicationTableCellStyle = lipgloss.NewStyle().Padding(0, 1)

func (m model) isOverlay() bool {
	return m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenInstallChannel || m.screen == screenUninstallConflictConfirm
}

func (m model) workspaceLines() ([]string, int) {
	values := m.visibleApplications()
	if m.isListScreen() {
		m.configureApplicationTable(values, max(1, viewWidth(m.width)-4), max(3, m.height-8))
	}
	if m.isListScreen() {
		lines := []string{m.theme.panel.Render("Applications")}
		if m.screen == screenAvailable {
			lines = append(lines, m.filterView())
		}
		if m.searching {
			lines = append(lines, m.searchInput.View())
		}
		lines = append(lines, strings.Split(m.applicationTable.View(), "\n")...)
		return lines, 3
	}
	return m.reviewLines(), 1
}

func (m *model) configureApplicationTable(values []app.Application, width, height int) {
	const columnCount = 6
	// Column widths describe content. Bubbles adds the shared cell padding
	// around every column, so reserve exactly that visible overhead here.
	_, paddingRight, _, paddingLeft := applicationTableCellStyle.GetPadding()
	contentWidth := max(1, width-columnCount*(paddingLeft+paddingRight))
	selectionWidth := 1
	statusWidth := 9 // longest status label is AVAILABLE
	channelWidth := 8
	nameWidth := 1
	versionWidth := 15
	versionBudget := max(2, contentWidth-nameWidth-selectionWidth-statusWidth-channelWidth)
	installedWidth := min(versionWidth, max(1, versionBudget/2))
	availableWidth := min(versionWidth, max(1, versionBudget-installedWidth))
	nameWidth = max(1, contentWidth-selectionWidth-statusWidth-installedWidth-availableWidth-channelWidth)
	rows := make([]table.Row, 0, len(values))
	for _, value := range values {
		marker := " "
		if m.selectedIDs[value.ID] {
			marker = "✓"
		}
		status := applicationStatus(value)
		if value.UpdateAvailable && !value.Pinned {
			status = "UPDATE"
		} else if value.InstalledVersion == "" {
			status = "AVAILABLE"
		} else if value.Pinned {
			status = "PINNED"
		} else {
			status = "INSTALLED"
		}
		channel := value.InstalledChannel
		if channel == "" {
			channel = value.DefaultChannel
		}
		rows = append(rows, table.Row{marker, value.Name, status, emptyDash(value.InstalledVersion), emptyDash(value.RegistryVersion), emptyDash(channel)})
	}
	if len(rows) == 0 {
		rows = append(rows, table.Row{"", "No applications."})
	}
	columns := []table.Column{
		{Title: "", Width: selectionWidth},
		{Title: "APPLICATION", Width: nameWidth},
		{Title: "STATUS", Width: statusWidth},
		{Title: "INSTALLED", Width: installedWidth},
		{Title: "AVAILABLE", Width: availableWidth},
		{Title: "CHANNEL", Width: channelWidth},
	}
	if len(values) == 0 {
		columns[1].Width = max(1, width-2*(paddingLeft+paddingRight)-selectionWidth)
		columns = columns[:2]
	}
	m.applicationTable.SetColumns(columns)
	m.applicationTable.SetRows(rows)
	m.applicationTable.SetWidth(max(1, width))
	m.applicationTable.SetHeight(max(2, height))
	cursor := 0
	if len(values) > 0 && m.cursorID != "" {
		for index, value := range values {
			if value.ID == m.cursorID {
				cursor = index
				break
			}
		}
	} else if len(rows) > 0 {
		cursor = min(m.selected, len(rows)-1)
	}
	if len(values) > 0 {
		m.cursorID = values[cursor].ID
	}
	m.applicationTable.SetCursor(max(0, cursor))
}

func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func (m model) reviewLines() []string {
	if len(m.selectedIDs) > 0 {
		lines := []string{m.theme.panel.Render("Review selection"), fmt.Sprintf("%d applications selected", len(m.selectedIDs))}
		if len(m.batchTargets) > 0 {
			lines = append(lines, "Versions locked for this operation:")
			for _, target := range m.batchTargets {
				lines = append(lines, fmt.Sprintf("  %s  %s  %s", target.Name, target.Version, target.Channel))
			}
		} else {
			for _, value := range m.reviewApplications() {
				lines = append(lines, "  "+value.Name+" · "+installedLabel(value))
			}
		}
		lines = append(lines, "", "Press Enter to apply or Esc to return.")
		return lines
	}
	if m.detail == nil {
		return []string{"Select an application to review."}
	}
	if m.screen == screenVersions {
		lines := []string{m.theme.panel.Render(m.detail.Name + " / Versions"), versionHeading(viewWidth(m.width))}
		for _, value := range m.versions {
			lines = append(lines, versionRow(value, viewWidth(m.width)))
		}
		return lines
	}
	lines := []string{m.theme.panel.Render("Review"), m.theme.accent.Render(m.detail.Name)}
	if m.detail.Summary != "" {
		lines = append(lines, m.detail.Summary)
	}
	lines = append(lines, "")
	addDetailFields(&lines, *m.detail, viewWidth(m.width), m.theme)
	lines = append(lines, "", "Press Enter to apply or Esc to return.")
	return lines
}

func (m model) reviewApplications() []app.Application {
	values := m.available
	if m.returnTo == screenInstalled || m.returnTo == screenUpdates {
		values = m.installed
		if m.returnTo == screenUpdates {
			values = updates(m.installed)
		}
	}
	result := make([]app.Application, 0, len(m.selectedIDs))
	for _, value := range values {
		if m.selectedIDs[value.ID] {
			result = append(result, value)
		}
	}
	return result
}

func (m model) overlayLines() []string {
	title, message := "", ""
	switch m.screen {
	case screenRollback:
		title, message = detailName(m.detail)+" / Rollback", "Roll back to the retained previous version?"
	case screenUninstall:
		title, message = detailName(m.detail)+" / Uninstall", "Remove this application and its integrations?"
	case screenUpgrade:
		title, message = "Upgrade TarLink", "Upgrade from "+m.tarlinkVersion.Current+" to "+m.tarlinkVersion.Latest+"?"
	case screenInstallConfirm:
		title, message = detailName(m.detail)+" / Install", "PATH conflicts found. Install anyway?"
	case screenInstallChannel:
		title, message = "Channel", "Choose a release channel"
	case screenUninstallConflictConfirm:
		title, message = "Remove conflict", "Remove this exact conflicting integration file?"
	}
	if title == "" {
		return nil
	}
	lines := []string{m.theme.panel.Render(title), "", message}
	if m.screen == screenInstallConfirm {
		for _, conflict := range m.pathConflicts {
			lines = append(lines, conflict.Type+": "+conflict.Directory+" "+conflict.Candidate)
		}
	}
	if m.screen == screenInstallChannel {
		for i, channel := range m.channels {
			prefix := "  "
			if i == m.channelSelected {
				prefix = "> "
			}
			lines = append(lines, prefix+channel)
		}
	}
	lines = append(lines, "", m.modalHelp(max(1, viewWidth(m.width)-8)))
	return lines
}

func (m model) modalHelp(width int) string {
	helper := m.help
	if helper.ShortSeparator == "" {
		helper = newHelp(m.color)
	}
	helper.SetWidth(width)
	return helper.FullHelpView([][]keypkg.Binding{m.actionBindings()})
}

func (m model) renderCard(content []string, width int) string {
	available := max(1, width-8)
	contentWidth := min(72, available)
	for _, line := range content {
		contentWidth = max(contentWidth, displaywidth.Options{ControlSequences: true}.String(line))
	}
	contentWidth = min(contentWidth, available)
	bounded := make([]string, len(content))
	for i, line := range content {
		bounded[i] = truncate(line, contentWidth, "…")
	}
	return m.theme.modal.Width(contentWidth).Render(strings.Join(bounded, "\n"))
}

func (m model) helpOverlayLines() []string {
	bindings := m.actionBindings()
	m.help.SetWidth(max(1, viewWidth(m.width)-4))
	return []string{m.theme.panel.Render("Keyboard reference"), m.help.FullHelpView([][]keypkg.Binding{bindings})}
}

func (m model) headerLine() string {
	left := m.theme.panel.Render("TarLink")
	count := len(updates(m.installed))
	right := fmt.Sprintf("Installed %d   Updates %d", len(m.installed), count)
	if viewWidth(m.width) < displaywidth.String(left)+displaywidth.String(right)+3 {
		return fit(left, m.width)
	}
	return left + strings.Repeat(" ", viewWidth(m.width)-displaywidth.String(left)-displaywidth.String(right)) + right
}

func (m model) tabsLine() string {
	labels := []string{"Browse", fmt.Sprintf("Installed %d", len(m.installed)), fmt.Sprintf("Updates %d", len(updates(m.installed)))}
	active := 0
	if m.screen == screenInstalled {
		active = 1
	}
	if m.screen == screenUpdates {
		active = 2
	}
	parts := make([]string, len(labels))
	for i, label := range labels {
		if i == active {
			parts[i] = m.theme.controlSelected.Render("[ " + label + " ]")
		} else {
			parts[i] = m.theme.control.Render(label)
		}
	}
	search := "[ / Search ]"
	if m.screen == screenUpdates {
		search = fmt.Sprintf("%d updates available", len(updates(m.installed))) + "   " + search
	}
	if m.searching {
		search = "[ / " + m.searchInput.View() + " ]"
	}
	if viewWidth(m.width) < displaywidth.String(strings.Join(parts, "   "))+displaywidth.String(search)+3 {
		return fit(strings.Join(parts, "  "), m.width)
	}
	return strings.Join(parts, "   ") + strings.Repeat(" ", max(1, viewWidth(m.width)-displaywidth.String(strings.Join(parts, "   "))-displaywidth.String(search))) + search
}

func (m model) separator() string { return strings.Repeat("─", viewWidth(m.width)) }
func detailName(value *app.Application) string {
	if value == nil {
		return ""
	}
	return value.Name
}

func (m model) isListScreen() bool {
	return m.screen == screenAvailable || m.screen == screenInstalled || m.screen == screenUpdates
}

func (m model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.initComponents()
	pressed := message.String()
	bindings := newKeyMap()
	if keypkg.Matches(message, bindings.CtrlC) {
		if m.opCancel != nil {
			m.opCancel()
		}
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if keypkg.Matches(message, bindings.Help) {
		m.helpOverlay = !m.helpOverlay
		return m, nil
	}
	if m.helpOverlay {
		if keypkg.Matches(message, bindings.Cancel) {
			m.helpOverlay = false
		}
		return m, nil
	}
	if m.searching {
		switch {
		case keypkg.Matches(message, bindings.Cancel):
			if !m.matchesAction(message, actionCancel) {
				return m, nil
			}
			m.searching = false
			m.searchInput.Blur()
			return m, nil
		case keypkg.Matches(message, bindings.Enter):
			if !m.matchesAction(message, actionEnter) {
				return m, nil
			}
			m.searching = false
			m.busy = "Searching"
			cmd, cancel := m.searchCmd()
			m.opCancel = cancel
			return m, cmd
		default:
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(message)
			m.query = m.searchInput.Value()
			return m, cmd
		}
	}

	if m.matchesAction(message, actionQuit) {
		if m.opCancel != nil {
			m.opCancel()
		}
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.busy != "" {
		if m.matchesAction(message, actionCancel) && m.opCancel != nil {
			m.opCancel()
		}
		return m, nil
	}
	if m.isListScreen() && (pressed == " " || pressed == "space") {
		visible := m.visibleApplications()
		cursor := m.applicationTable.Cursor()
		if len(visible) > 0 && cursor < len(visible) {
			if m.selectedIDs == nil {
				m.selectedIDs = map[string]bool{}
			}
			id := visible[cursor].ID
			if m.selectedIDs[id] {
				delete(m.selectedIDs, id)
			} else {
				m.selectedIDs[id] = true
			}
		}
		return m, nil
	}
	switch {
	case m.matchesAction(message, actionUpgrade):
		if m.upgradeAvailable {
			m.clearFeedback()
			m.returnTo = m.screen
			m.confirmSet = false
			m.screen = screenUpgrade
		}
	case m.matchesAction(message, actionUp):
		m.clearFeedback()
		delta := -1
		if keypkg.Matches(message, bindings.Down) {
			delta = 1
		}
		if m.screen == screenInstallChannel {
			m.moveChannel(delta)
		} else {
			m.moveSelection(delta)
		}
	case m.matchesAction(message, actionDown):
		m.clearFeedback()
		if m.screen == screenInstallChannel {
			m.moveChannel(1)
		} else {
			m.moveSelection(1)
		}
	case m.matchesAction(message, actionFilter) && keypkg.Matches(message, bindings.Left):
		if m.screen == screenAvailable {
			m.clearFeedback()
			m.changeFilter(-1)
		}
	case m.matchesAction(message, actionFilter) && keypkg.Matches(message, bindings.Right):
		if m.screen == screenAvailable {
			m.clearFeedback()
			m.changeFilter(1)
		}
	case m.matchesAction(message, actionSearch):
		m.clearFeedback()
		m.screen = screenAvailable
		m.searching = true
		m.query = ""
		m.searchInput.SetValue("")
		m.searchInput.Focus()
		m.selected = 0
	case m.matchesAction(message, actionInstalled):
		m.clearFeedback()
		m.selectedIDs = nil
		m.screen = screenInstalled
		m.selected = 0
	case m.matchesAction(message, actionUpdates):
		m.clearFeedback()
		m.selectedIDs = nil
		m.screen = screenUpdates
		m.selected = 0
	case m.matchesAction(message, actionCancel):
		m.clearFeedback()
		if m.screen == screenInstallChannel {
			m.screen = screenDetails
		} else if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenUninstallConflictConfirm {
			m.screen = m.confirmationTarget()
			m.confirmSet = false
			if m.screen != screenDetails {
				m.detail = nil
			}
		} else if m.screen == screenDetails || m.screen == screenVersions {
			if m.screen == screenVersions && m.versionsFromDetails {
				m.screen = screenDetails
			} else {
				m.screen = m.returnTo
				if m.screen != screenDetails {
					m.detail = nil
				}
			}
			m.versions = nil
			m.versionsFromDetails = false
		} else {
			m.screen = screenAvailable
			m.selected = 0
			m.selectedIDs = nil
		}
	case m.matchesAction(message, actionEnter):
		if m.screen == screenInstallChannel {
			if m.detail != nil && len(m.channels) > 0 && m.channelSelected < len(m.channels) {
				m.clearFeedback()
				m.busy = "Checking installation path"
				cmd, cancel := m.pathCheckCmd(m.detail.ID)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenUpgrade {
			m.busy = "Upgrading TarLink"
			m.startOperation()
			cmd, cancel := m.upgradeCmd()
			m.opCancel = cancel
			return m, cmd
		}
		if m.screen == screenRollback {
			if id := m.selectedID(); id != "" {
				m.busy = "Rolling back"
				m.startOperation()
				cmd, cancel := m.rollbackCmd(id)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenUninstall {
			if len(m.batchIDs) > 0 {
				m.busy = "Uninstalling selected applications"
				m.startOperation()
				cmd, cancel := m.batchUninstallCmd(m.batchIDs)
				m.opCancel = cancel
				return m, cmd
			}
			if id := m.selectedID(); id != "" && m.selectedInstalled() {
				m.uninstallConflict = nil
				m.busy = "Uninstalling"
				m.startOperation()
				cmd, cancel := m.uninstallCmd(id)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenUninstallConflictConfirm {
			if m.uninstallConflict != nil {
				m.busy = "Removing conflicting file"
				m.startOperation()
				cmd, cancel := m.removeUninstallConflictCmd(*m.uninstallConflict)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenInstallConfirm {
			if len(m.batchTargets) > 0 {
				m.busy = "Installing selected applications"
				m.startOperation()
				cmd, cancel := m.batchInstallCmd(m.batchIDs)
				m.opCancel = cancel
				return m, cmd
			}
			if id := m.selectedID(); id != "" {
				m.busy = "Installing " + id
				m.startOperation()
				cmd, cancel := m.installCmd(m.installSelector(id))
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenDetails {
			if len(m.selectedIDs) > 0 {
				if m.screen == screenDetails && m.returnTo == screenAvailable {
					if len(m.batchTargets) > 0 {
						m.busy = "Installing selected applications"
						m.startOperation()
						cmd, cancel := m.batchInstallCmd(m.batchIDs)
						m.opCancel = cancel
						return m, cmd
					}
					return m.startBatchInstall()
				}
				if m.returnTo == screenInstalled {
					m.batchIDs = m.selectedIDsInOrder(m.installed)
					m.busy = "Uninstalling selected applications"
					m.startOperation()
					cmd, cancel := m.batchUninstallCmd(m.batchIDs)
					m.opCancel = cancel
					return m, cmd
				}
			}
			if m.detail != nil && m.detail.InstalledVersion == "" && len(m.detail.ChannelHeads) > 1 {
				m.channels = channelNames(m.detail)
				m.channelSelected = channelIndex(m.channels, m.detail.DefaultChannel)
				m.screen = screenInstallChannel
				return m, nil
			}
			m.channels = nil
			m.channelSelected = 0
			return m.activateSelected()
		}
		visible := m.visibleApplications()
		if len(visible) != 0 {
			m.clearFeedback()
			selected := visible[m.selected]
			if len(m.selectedIDs) == 0 {
				m.detail = &selected
			}
			m.returnTo = m.screen
			m.screen = screenDetails
			return m, nil
		}
	case m.matchesAction(message, actionVersions):
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.clearFeedback()
			m.setDetail(id)
			if m.screen == screenDetails {
				m.versionsFromDetails = true
			} else if m.screen != screenVersions {
				m.returnTo = m.screen
				m.versionsFromDetails = false
			}
			m.busy = "Loading versions"
			cmd, cancel := m.versionsCmd(id)
			m.opCancel = cancel
			return m, cmd
		}
	case m.matchesAction(message, actionRollback):
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.clearFeedback()
			m.setDetail(id)
			if m.screen != screenDetails && m.screen != screenVersions {
				m.returnTo = m.screen
			}
			m.confirmTo = m.screen
			m.confirmSet = true
			m.screen = screenRollback
		}
	case m.matchesAction(message, actionUninstall):
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.clearFeedback()
			m.setDetail(id)
			if m.screen != screenDetails && m.screen != screenVersions {
				m.returnTo = m.screen
			}
			m.confirmTo = m.screen
			m.confirmSet = true
			m.screen = screenUninstall
		}
	case m.matchesAction(message, actionRemoveConflict):
		if m.screen == screenUninstall && m.uninstallConflict != nil {
			m.confirmTo = screenUninstall
			m.confirmSet = true
			m.screen = screenUninstallConflictConfirm
		}
	}
	m.clampSelection()
	return m, nil
}

func (m *model) moveSelection(delta int) {
	length := len(m.visibleApplications())
	if length == 0 {
		m.selected = 0
		return
	}
	if delta < 0 {
		m.applicationTable.MoveUp(-delta)
	} else {
		m.applicationTable.MoveDown(delta)
	}
	m.selected = m.applicationTable.Cursor()
	values := m.visibleApplications()
	if m.selected >= 0 && m.selected < len(values) {
		m.cursorID = values[m.selected].ID
	}
}

func (m *model) moveChannel(delta int) {
	if len(m.channels) == 0 {
		m.channelSelected = 0
		return
	}
	m.channelSelected += delta
	if m.channelSelected < 0 {
		m.channelSelected = 0
	}
	if m.channelSelected >= len(m.channels) {
		m.channelSelected = len(m.channels) - 1
	}
}

func channelNames(value *app.Application) []string {
	channels := make([]string, 0, len(value.ChannelHeads))
	for channel := range value.ChannelHeads {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	return channels
}

func channelIndex(channels []string, preferred string) int {
	for index, channel := range channels {
		if channel == preferred {
			return index
		}
	}
	return 0
}

func (m model) activateSelected() (tea.Model, tea.Cmd) {
	if m.detail == nil {
		return m, nil
	}
	id := m.detail.ID
	switch {
	case m.detail.InstalledVersion == "":
		m.clearFeedback()
		m.busy = "Checking installation path"
		cmd, cancel := m.pathCheckCmd(id)
		m.opCancel = cancel
		return m, cmd
	case m.detail.Pinned:
		m.clearFeedback()
		m.status = id + " is pinned at " + m.detail.InstalledVersion
		return m, nil
	case m.detail.UpdateAvailable:
		m.busy = "Updating " + id
		m.startOperation()
		cmd, cancel := m.updateCmd(id)
		m.opCancel = cancel
		return m, cmd
	default:
		m.clearFeedback()
		m.status = id + " is already up to date"
		return m, nil
	}
}

// startInstall begins the install operation directly after a PATH check found
// no conflicts. It is called from the path-check completion handler.
func (m model) startInstall(id string) (tea.Model, tea.Cmd) {
	m.busy = "Installing " + id
	m.startOperation()
	cmd, cancel := m.installCmd(id)
	m.opCancel = cancel
	return m, cmd
}

func (m model) startBatchInstall() (tea.Model, tea.Cmd) {
	service, ok := m.service.(app.BatchService)
	if !ok {
		m.err = errors.New("batch operations are unavailable")
		return m, nil
	}
	m.batchIDs = m.selectedIDsInOrder(m.available)
	m.busy = "Resolving selected applications"
	cmd, cancel := m.cancellableCmd(func(ctx context.Context) tea.Msg {
		targets, err := service.ResolveInstallBatch(ctx, m.batchIDs)
		return batchResolveMsg{targets: targets, err: err}
	}, batchResolveMsg{err: context.Canceled})
	m.opCancel = cancel
	return m, cmd
}

func (m model) selectedIDsInOrder(values []app.Application) []string {
	result := make([]string, 0, len(m.selectedIDs))
	seen := map[string]bool{}
	for _, value := range values {
		if m.selectedIDs[value.ID] {
			result = append(result, value.ID)
			seen[value.ID] = true
		}
	}
	hidden := make([]string, 0)
	for id := range m.selectedIDs {
		if !seen[id] {
			hidden = append(hidden, id)
		}
	}
	sort.Strings(hidden)
	result = append(result, hidden...)
	return result
}

func (m model) pathCheckCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.cancellableCmd(func(ctx context.Context) tea.Msg {
		conflicts, err := m.service.CheckInstallPath(id)
		return pathCheckMsg{appID: id, conflicts: conflicts, err: err}
	}, pathCheckMsg{err: context.Canceled})
}

func (m model) loadCmd() tea.Cmd {
	return func() tea.Msg {
		available, err := m.service.Search(m.ctx, m.query)
		if err != nil {
			return loadedMsg{err: err}
		}
		installed, err := m.service.List(m.ctx)
		return loadedMsg{available: available, installed: installed, err: err}
	}
}

func (m model) checkVersionCmd() tea.Cmd {
	return func() tea.Msg {
		value, err := m.service.CheckTarLinkVersionFresh(m.ctx)
		return versionMsg{value: value, err: err}
	}
}

func (m model) upgradeCmd() (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		value, err := m.service.UpgradeTarLink(ctx, sink)
		message := ""
		if err == nil {
			message = "TarLink upgraded to " + value.Latest + ". The new version will be used the next time TarLink starts."
		}
		return operationMsg{message: message, next: screenAvailable, move: true, clearUpgrade: err == nil}, err
	})
}

func (m model) searchCmd() (tea.Cmd, context.CancelFunc) {
	return m.cancellableCmd(func(ctx context.Context) tea.Msg {
		values, err := m.service.Search(ctx, m.query)
		return searchMsg{values: values, err: err}
	}, searchMsg{cancelled: true})
}

func (m model) versionsCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.cancellableCmd(func(ctx context.Context) tea.Msg {
		values, err := m.service.Versions(ctx, id)
		return versionsMsg{values: values, err: err}
	}, versionsMsg{cancelled: true})
}

func (m model) installCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.Install(ctx, id, sink)
		return operationMsg{message: resultMessage("Installed", result)}, err
	})
}

func (m model) batchInstallCmd(ids []string) (tea.Cmd, context.CancelFunc) {
	service := m.service.(app.BatchService)
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := service.InstallBatch(ctx, ids, sink)
		return operationMsg{message: batchMessage("Installed", result), move: true, next: screenAvailable}, err
	})
}

func (m model) installSelector(id string) string {
	if m.detail == nil || m.detail.ID != id || len(m.channels) == 0 || m.channelSelected < 0 || m.channelSelected >= len(m.channels) {
		return id
	}
	channel := m.channels[m.channelSelected]
	if _, ok := m.detail.ChannelHeads[channel]; !ok {
		return id
	}
	return id + "@" + channel
}

func (m model) updateCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.Update(ctx, id, sink)
		return operationMsg{message: resultMessage("Updated", result)}, err
	})
}

func (m model) rollbackCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.Rollback(ctx, id, sink)
		return operationMsg{message: resultMessage("Rolled back", result), next: screenDetails, move: true}, err
	})
}

func (m model) operationCmd(operation func(context.Context, app.ProgressSink) (operationMsg, error)) (tea.Cmd, context.CancelFunc) {
	base := m.ctx
	if base == nil {
		base = context.Background()
	}
	opCtx, opCancel := context.WithCancel(base)
	hub := &operationHub{wake: make(chan struct{}, 1), result: make(chan operationMsg, 1), ctx: opCtx}
	cmd := func() tea.Msg {
		go func() {
			sink := func(event app.Progress) {
				hub.publish(event)
			}
			result, err := operation(opCtx, sink)
			result.err = err
			hub.finish(result)
		}()
		return hub.next(opCtx)
	}
	return cmd, opCancel
}

// cancellableCmd runs a non-progress command (search, versions) in a
// per-operation context so it can be cancelled via Esc. It returns the
// produced message on completion, or the cancelled message once the context is
// cancelled.
func (m model) cancellableCmd(run func(ctx context.Context) tea.Msg, cancelled tea.Msg) (tea.Cmd, context.CancelFunc) {
	base := m.ctx
	if base == nil {
		base = context.Background()
	}
	opCtx, opCancel := context.WithCancel(base)
	done := make(chan tea.Msg, 1)
	cmd := func() tea.Msg {
		go func() { done <- run(opCtx) }()
		select {
		case msg := <-done:
			return msg
		case <-opCtx.Done():
			return cancelled
		}
	}
	return cmd, opCancel
}

func waitProgress(hub *operationHub) tea.Cmd {
	return func() tea.Msg {
		return hub.next(hub.ctx)
	}
}

func (h *operationHub) publish(event app.Progress) {
	h.mu.Lock()
	if event.Stage != "" && event.Stage != h.lastStage {
		h.latest = nil
		h.lastStage = event.Stage
		h.pending = append(h.pending, event)
	} else {
		copy := event
		h.latest = &copy
	}
	h.mu.Unlock()
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *operationHub) finish(result operationMsg) {
	h.result <- result
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *operationHub) next(ctx context.Context) tea.Msg {
	for {
		h.mu.Lock()
		if len(h.pending) > 0 {
			event := h.pending[0]
			h.pending = h.pending[1:]
			h.mu.Unlock()
			return progressMsg{hub: h, event: event}
		}
		if h.latest != nil {
			if !h.lastEmit.IsZero() && time.Since(h.lastEmit) < 200*time.Millisecond {
				wait := 200*time.Millisecond - time.Since(h.lastEmit)
				h.mu.Unlock()
				timer := time.NewTimer(wait)
				select {
				case result := <-h.result:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					h.mu.Lock()
					if h.latest != nil {
						event := *h.latest
						h.latest = nil
						h.lastEmit = time.Now()
						h.mu.Unlock()
						h.result <- result
						return progressMsg{hub: h, event: event}
					}
					h.mu.Unlock()
					return result
				case <-timer.C:
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return operationMsg{err: ctx.Err()}
				}
				continue
			}
			event := *h.latest
			h.latest = nil
			h.lastEmit = time.Now()
			h.mu.Unlock()
			return progressMsg{hub: h, event: event}
		}
		h.mu.Unlock()
		select {
		case result := <-h.result:
			return result
		case <-h.wake:
		case <-ctx.Done():
			return operationMsg{err: ctx.Err()}
		}
	}
}

func (m model) uninstallCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		err := m.service.Uninstall(ctx, id, sink)
		next := m.confirmationTarget()
		if next == screenVersions {
			next = screenDetails
		}
		result := operationMsg{message: "Uninstalled " + id, next: next, move: true}
		var typed *app.UninstallConflictError
		if errors.As(err, &typed) {
			conflict := typed.Conflict
			result.conflict = &conflict
		}
		return result, err
	})
}

func (m model) removeUninstallConflictCmd(conflict app.UninstallConflict) (tea.Cmd, context.CancelFunc) {
	recovery, ok := m.service.(app.UninstallRecoveryService)
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		if !ok {
			return operationMsg{}, errors.New("uninstall conflict recovery is unavailable")
		}
		if err := recovery.RemoveUninstallConflict(ctx, conflict.AppID, conflict.Path); err != nil {
			return operationMsg{}, err
		}
		err := m.service.Uninstall(ctx, conflict.AppID, sink)
		next := m.confirmationTarget()
		if next == screenVersions {
			next = screenDetails
		}
		result := operationMsg{message: "Uninstalled " + conflict.AppID, next: next, move: true}
		var typed *app.UninstallConflictError
		if errors.As(err, &typed) {
			nextConflict := typed.Conflict
			result.conflict = &nextConflict
		}
		return result, err
	})
}

func (m model) batchUninstallCmd(ids []string) (tea.Cmd, context.CancelFunc) {
	service := m.service.(app.BatchService)
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := service.UninstallBatch(ctx, ids, sink)
		return operationMsg{message: batchMessage("Uninstalled", result), move: true, next: screenInstalled}, err
	})
}

func batchMessage(verb string, result app.BatchResult) string {
	message := fmt.Sprintf("%s: %d", verb, len(result.Completed))
	if len(result.Failed) > 0 {
		message += fmt.Sprintf(" · Failed: %d", len(result.Failed))
	}
	return message
}

func (m model) confirmationTarget() screen {
	if m.confirmSet {
		return m.confirmTo
	}
	return m.returnTo
}

func (m *model) setDetail(id string) {
	for _, value := range m.installed {
		if value.ID == id {
			copy := value
			m.detail = &copy
			return
		}
	}
}

func (m *model) refreshDetail() {
	if m.detail == nil {
		return
	}
	id := m.detail.ID
	for _, value := range m.available {
		if value.ID == id {
			copy := value
			m.detail = &copy
			return
		}
	}
	for _, value := range m.installed {
		if value.ID == id {
			copy := value
			m.detail = &copy
			return
		}
	}
	m.detail = nil
}

func (m model) visibleApplications() []app.Application {
	switch m.screen {
	case screenInstalled:
		return m.installed
	case screenUpdates:
		return updates(m.installed)
	}
	if m.applicationFilter == filterAll {
		return m.available
	}
	visible := make([]app.Application, 0, len(m.available))
	for _, value := range m.available {
		installed := value.InstalledVersion != ""
		if (m.applicationFilter == filterInstalled && installed) ||
			(m.applicationFilter == filterNotInstalled && !installed) {
			visible = append(visible, value)
		}
	}
	return visible
}

func (m model) filterView() string {
	labels := [...]string{"All", "Installed", "Not installed"}
	var parts []string
	for index, label := range labels {
		if applicationFilter(index) == m.applicationFilter {
			parts = append(parts, m.theme.controlSelected.Render("[ "+label+" ]"))
		} else {
			parts = append(parts, m.theme.control.Render(label))
		}
	}
	return strings.Join(parts, "  ")
}

func (m *model) changeFilter(delta int) {
	value := int(m.applicationFilter) + delta
	if value < 0 {
		value = int(filterNotInstalled)
	} else if value > int(filterNotInstalled) {
		value = int(filterAll)
	}
	m.applicationFilter = applicationFilter(value)
	m.selected = 0
}

func (m model) selectedID() string {
	if m.detail != nil && (m.screen == screenDetails || m.screen == screenVersions || m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenInstallConfirm) {
		return m.detail.ID
	}
	visible := m.visibleApplications()
	if len(visible) == 0 || m.selected >= len(visible) {
		return ""
	}
	return visible[m.selected].ID

}

func (m model) selectedInstalled() bool {
	id := m.selectedID()
	for _, value := range m.installed {
		if value.ID == id {
			return true
		}
	}
	return false
}

func (m *model) clampSelection() {
	length := len(m.visibleApplications())
	if length == 0 {
		m.selected = 0
	} else if m.selected >= length {
		m.selected = length - 1
	}
	if m.selected >= 0 && m.selected < length {
		m.cursorID = m.visibleApplications()[m.selected].ID
	}
	m.applicationTable.SetCursor(m.selected)
}

func updates(values []app.Application) []app.Application {
	result := make([]app.Application, 0, len(values))
	for _, value := range values {
		if value.UpdateAvailable && !value.Pinned {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func addDetailFields(lines *[]string, value app.Application, width int, theme tuiTheme) {
	field := func(label, content string) {
		if content == "" {
			return
		}
		if viewWidth(width) < 40 {
			*lines = append(*lines, label, fit(content, width))
		} else {
			*lines = append(*lines, fmt.Sprintf("%-14s %s", label, fit(content, max(1, width-15))))
		}
	}
	field("Version", value.InstalledVersion)
	field("Available", value.RegistryVersion)
	field("Channel", value.InstalledChannel)
	if value.Pinned {
		field("State", "Pinned")
	} else {
		field("State", applicationStatus(value))
	}
	field("Categories", strings.Join(value.Categories, ", "))
	if hasGameData(value) {
		field("Requires", "Original game data")
	}
	if value.Homepage != "" {
		*lines = append(*lines, "", "Homepage", fit(value.Homepage, width))
	}
	if viewWidth(width) >= 60 && value.ID != "" {
		*lines = append(*lines, "", theme.muted.Render("ID: "+value.ID))
	}
}

func versionHeading(width int) string {
	if viewWidth(width) < 40 {
		return "VERSION"
	}
	return "VERSION" + strings.Repeat(" ", max(1, viewWidth(width)/2-7)) + "STATUS"
}
func versionRow(value app.Version, width int) string {
	if viewWidth(width) < 40 {
		return truncate(value.Version, width, "…")
	}
	return truncate(value.Version, max(1, width/2), "…") + strings.Repeat(" ", max(1, width/2-displaywidth.String(truncate(value.Version, max(1, width/2), "…")))) + truncate(value.Status, max(1, width/2-1), "…")
}

func installedLabel(value app.Application) string {
	label := applicationStatus(value)
	if value.InstalledChannel != "" {
		label += " · " + value.InstalledChannel
	}
	if value.Pinned && label != "Pinned" {
		label += " · Pinned"
	}
	if hasGameData(value) {
		label += " · Game data required"
	}
	return label
}

func applicationStatus(value app.Application) string {
	if value.InstalledVersion == "" {
		return "Not installed"
	}
	if value.Pinned {
		return "Pinned"
	}
	if value.UpdateAvailable {
		return "Update available"
	}
	return "Installed"
}

func hasGameData(value app.Application) bool {
	for _, requirement := range value.Requirements {
		if requirement == "original-game-data" {
			return true
		}
	}
	return false
}

func resultMessage(action string, result app.Result) string {
	if result.AppID == "" {
		return ""
	}
	return fmt.Sprintf("%s %s %s", action, result.AppID, result.Version)
}

type tone uint8

const (
	accent tone = iota
	success
	warning
	danger
	muted
)

func (m model) style(value string, valueTone tone) string {
	return m.theme.render(value, valueTone)
}

func colorEnabled(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

func viewWidth(width int) int {
	if width <= 0 {
		return 80
	}
	return width
}

func fit(value string, width int) string {
	return truncate(value, viewWidth(width), "…")
}

func truncate(value string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	return displaywidth.Options{ControlSequences: true}.TruncateString(value, width, tail)
}

func (m model) progressLine() string {
	stage := title(string(m.progress.Stage))
	if m.progress.Description != "" {
		stage = m.progress.Description
	}
	if m.progress.Stage == app.ProgressComplete {
		return m.style(stage, success)
	}
	line := stage
	if m.progress.Item > 0 && m.progress.Total > 0 && m.progress.AppID != "" {
		line = fmt.Sprintf("%s %d/%d · %s", stage, m.progress.Item, m.progress.Total, m.progress.AppID)
	}
	if m.progress.BytesTotal > 0 {
		done := m.progress.BytesDone
		if done < 0 {
			done = 0
		}
		if done > m.progress.BytesTotal {
			done = m.progress.BytesTotal
		}
		percent := float64(done) / float64(m.progress.BytesTotal)
		bar := m.progressBar
		if bar.Width() == 0 {
			bar = newProgress(m.color)
		}
		bar.SetWidth(progressBarWidthFor(m.width))
		line += " " + bar.ViewAs(percent)
		if viewWidth(m.width) >= 60 {
			line += "  " + fmt.Sprintf("%s / %s", bytesLabel(m.progress.BytesDone), bytesLabel(m.progress.BytesTotal))
		}
	} else if m.progress.BytesDone > 0 {
		line += " " + bytesLabel(m.progress.BytesDone)
	}
	if m.progressSpeed > 0 {
		line += "  " + formatRate(m.progressSpeed)
		if m.estimator.Ready() && m.progress.BytesTotal > m.progress.BytesDone && m.progress.BytesTotal > 0 {
			line += " · ETA ~" + formatDuration(float64(m.progress.BytesTotal-m.progress.BytesDone)/m.progressSpeed)
		}
	}
	return m.style(line, accent)
}

func (m *model) resetProgress() {
	m.progress = app.Progress{}
	m.progressSpeed = 0
	m.progressSpeedAt = time.Time{}
	m.estimator.Reset()
}

// clearFeedback deterministically clears stale success/error feedback when the
// user navigates away from a completed operation's result.
func (m *model) clearFeedback() {
	m.status = ""
	m.err = nil
}

// startOperation resets progress and clears stale feedback before a new
// active operation begins, so a previous operation's success/error never
// leaks into the next one.
func (m *model) startOperation() {
	m.clearFeedback()
	m.resetProgress()
}

func bytesLabel(value int64) string {
	if value < 0 {
		value = 0
	}
	units := []string{"B", "KiB", "MiB", "GiB"}
	amount := float64(value)
	i := 0
	for amount >= 1024 && i < len(units)-1 {
		amount /= 1024
		i++
	}
	if i == 0 || amount >= 10 {
		return fmt.Sprintf("%.0f %s", amount, units[i])
	}
	return fmt.Sprintf("%.1f %s", amount, units[i])
}

func title(value string) string {
	if value == "" {
		return value
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
