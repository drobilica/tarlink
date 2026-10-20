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
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/displaywidth"
	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/version"
)

type screen uint8

const (
	screenAvailable screen = iota
	screenInstalled
	screenNotInstalled
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

type loadedMsg struct {
	available  []app.Application
	installed  []app.Application
	err        error
	generation uint64
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

type updatePlanMsg struct {
	plan      app.UpdatePlan
	err       error
	cancelled bool
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
	generation   uint64
	appID        string
	partial      bool
}

type progressMsg struct {
	hub        *operationHub
	event      app.Progress
	generation uint64
}

type operationHub struct {
	mu         sync.Mutex
	pending    []app.Progress
	latest     *app.Progress
	lastStage  app.ProgressStage
	lastEmit   time.Time
	wake       chan struct{}
	result     chan operationMsg
	ctx        context.Context
	generation uint64
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
	dataLoaded          bool
	loading             bool
	versions            []app.Version
	selected            int
	cursorID            string
	selectedIDs         map[string]bool
	batchIDs            []string
	batchTargets        []app.BatchTarget
	updatePlan          *app.UpdatePlan
	channelSelected     int
	channels            []string
	detail              *app.Application
	searching           bool
	query               string
	searchFrom          screen
	searchPriorQuery    string
	searchPriorSelected int
	searchPriorCursorID string
	busy                string
	status              string
	feedbackAppID       string
	feedbackPartial     bool
	err                 error
	width               int
	height              int
	progress            app.Progress
	progressSpeed       float64
	progressSpeedAt     time.Time
	estimator           speedEstimator
	operationGeneration uint64
	requestGeneration   uint64
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
	tableConfigured     bool
	tableWidth          int
	tableHeight         int
	searchInput         textinput.Model
	reviewScroll        int
	overlayScroll       int
}

// Run starts the terminal renderer. All application changes are delegated to
// the same service API used by the CLI.
func Run(ctx context.Context, service app.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return fmt.Errorf("TarLink core is unavailable")
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	m := model{ctx: operationContext, service: service, screen: screenAvailable, loading: true, color: colorEnabled(output), theme: newTheme(colorEnabled(output)), help: newHelp(colorEnabled(output)), progressBar: newProgress(colorEnabled(output)), cancel: cancel}
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
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampSelection()
		if m.screen == screenDetails || m.screen == screenVersions {
			v := m.reviewViewport(max(1, m.height-4))
			m.reviewScroll = v.YOffset()
		}
		m.help.SetWidth(m.width)
		m.progressBar.SetWidth(progressBarWidthFor(m.width))
		m.searchInput.SetWidth(max(12, m.width-18))
		m.rebuildApplicationTable()
		return m, nil
	case loadedMsg:
		if message.generation != m.requestGeneration {
			return m, nil
		}
		m.busy = ""
		m.loading = false
		m.err = message.err
		// Keep previously loaded counts during a refresh failure; only the
		// initial unavailable state suppresses aggregate counts.
		m.dataLoaded = m.dataLoaded || message.err == nil
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
		m.rebuildApplicationTable()
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
			m.confirmTo = m.screen
			m.confirmSet = true
			m.screen = screenInstallConfirm
			return m, nil
		}
		return m.startInstall(m.installSelector(message.appID))
	case updatePlanMsg:
		m.busy = ""
		m.opCancel = nil
		m.updatePlan = nil
		if message.cancelled {
			m.status = "Update planning cancelled"
			return m, nil
		}
		if message.err != nil {
			m.err = message.err
			return m, nil
		}
		m.updatePlan = &message.plan
		m.reviewScroll = 0
		m.screen = screenDetails
		return m, nil
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
		m.clampSelection()
		m.rebuildApplicationTable()
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
		if message.generation != m.operationGeneration || m.opCancel == nil {
			return m, nil
		}
		m.busy = ""
		m.resetProgress()
		m.opCancel = nil
		m.err = message.err
		m.uninstallConflict = message.conflict
		m.status = message.message
		m.feedbackAppID = message.appID
		m.feedbackPartial = message.partial
		if errors.Is(message.err, context.Canceled) {
			m.err = nil
			m.status = "Operation cancelled"
			m.batchIDs, m.batchTargets = nil, nil
			m.selectedIDs = nil
			m.rebuildApplicationTable()
			return m, nil
		}
		if message.err != nil {
			m.status = ""
			m.rebuildApplicationTable()
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
		m.rebuildApplicationTable()
		return m, m.loadCmd()
	case progressMsg:
		if message.generation != m.operationGeneration || m.opCancel == nil {
			return m, nil
		}
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
		// Configure the table before the first navigation so models that never
		// saw a window-size message still move the cursor.
		if m.isListScreen() && !m.tableConfigured {
			m.rebuildApplicationTable()
		}
		updated, command := m.updateKey(message)
		next := updated.(model)
		// Keys other than pure cursor movement can change the rows, markers,
		// screen, filter, or workspace budget, so rebuild the table for them.
		// Cursor navigation instead reuses the configured rows, keeping the
		// Bubbles viewport scroll offset and skipping a full row rebuild per
		// keypress.
		if next.isListScreen() && !next.searching && !next.cursorMovement(message) {
			next.rebuildApplicationTable()
		}
		return next, command
	}
	return m, nil
}

func (m model) View() tea.View {
	m.initComponents()
	body := m.bodyLines()
	footer := fit(m.helpView(), m.width)
	if m.height > 0 {
		body = append(body, make([]string, max(0, m.height-1-len(body)))...)
	}
	lines := append(body, footer)
	view := tea.NewView(strings.Join(lines, "\n") + "\n")
	view.AltScreen = true
	return view
}

// bodyLines is the shared shell and screen content.
func (m model) bodyLines() []string {
	if m.width > 0 && m.height > 0 && (m.width < 30 || m.height < 10) {
		return []string{fit("TarLink — enlarge terminal", m.width)}
	}
	lines := []string{m.headerLine()}
	lines = append(lines, strings.Split(m.tabsLine(), "\n")...)
	lines = append(lines, m.separator())
	add := func(value string) {
		for _, part := range strings.Split(value, "\n") {
			lines = append(lines, fit(part, m.width))
		}
	}
	showFeedback := m.feedbackAppID == "" || m.isListScreen() || (m.detail != nil && m.detail.ID == m.feedbackAppID)
	if m.status != "" && showFeedback && !(m.feedbackAppID != "" && (m.screen == screenDetails || m.screen == screenVersions)) {
		tone := success
		if m.feedbackPartial {
			tone = warning
		}
		add(m.style(m.status, tone))
	}
	if m.err != nil && showFeedback && !(m.feedbackAppID != "" && (m.screen == screenDetails || m.screen == screenVersions)) {
		add(m.style("Operation failed", danger))
		add(m.style(m.err.Error(), danger))
	}
	if m.uninstallConflict != nil && (m.screen == screenUninstall || m.screen == screenUninstallConflictConfirm) {
		add("Conflicting integration: " + m.uninstallConflict.Path)
	}
	remaining := max(0, m.height-len(lines)-1)
	var expanded []string
	if m.helpOverlay {
		expanded = m.helpOverlayLines()
		remaining, expanded = applyHelpOverlayBudget(remaining, expanded)
	}
	workspace, _ := m.workspaceLinesFor(remaining)
	if m.isOverlay() && m.busy == "" && remaining < 5 {
		if remaining > 0 {
			lines = append(lines, fit("Modal open · Esc closes Help", m.width))
		}
	} else if m.isOverlay() && m.busy == "" {
		contentWidth, _ := m.modalContent(m.overlayLines(), viewWidth(m.width))
		innerHeight := max(1, remaining-4)
		card := m.theme.modal.Width(contentWidth).Render(m.modalViewport(innerHeight).View())
		for strings.Count(card, "\n")+1 > remaining && innerHeight > 1 {
			innerHeight--
			card = m.theme.modal.Width(contentWidth).Render(m.modalViewport(innerHeight).View())
		}
		placed := lipgloss.Place(viewWidth(m.width), max(1, remaining), lipgloss.Center, lipgloss.Center, card)
		for _, line := range strings.Split(placed, "\n") {
			if len(lines) >= m.height-1 {
				break
			}
			lines = append(lines, fit(line, m.width))
		}
	} else {
		for _, line := range workspace {
			lines = append(lines, lipgloss.NewStyle().PaddingLeft(1).Render(fit(line, max(1, viewWidth(m.width)-1))))
		}
	}
	if m.helpOverlay {
		lines = append(lines, "")
		for _, line := range expanded {
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

func (m model) workspaceLinesFor(budget int) ([]string, int) {
	values := m.visibleApplications()
	if budget <= 0 {
		return nil, 0
	}
	if m.busy != "" {
		lines := []string{m.theme.panel.Render(m.busy)}
		if m.progress.AppID != "" {
			lines = append(lines, m.progress.AppID)
		}
		if detail := m.detail; detail != nil && m.progress.AppID == "" && m.operationGeneration > 0 {
			lines = append(lines, detail.Name)
		}
		if progress := m.progressLine(); progress != "" {
			lines = append(lines, "", progress)
		}
		if len(lines) > budget {
			lines = lines[:budget]
		}
		return lines, 1
	}
	if m.isListScreen() && m.loading && !m.dataLoaded {
		return []string{m.theme.accent.Render("Loading applications…")}, 1
	}
	if m.isListScreen() && !m.dataLoaded && m.err != nil {
		return []string{m.theme.danger.Render("Application data unavailable.")}, 1
	}
	previewRows := 0
	if m.isListScreen() {
		tableWidth, tableHeight, tablePreviewRows := m.applicationTableSize(values, budget)
		previewRows = tablePreviewRows
		// Render the configured table so the viewport scroll offset that
		// Bubbles maintains during navigation survives rendering. Fall back
		// to a local configuration only for models that never passed through
		// Update or whose workspace budget changed since the configuration.
		if !m.tableConfigured || m.tableWidth != tableWidth || m.tableHeight != tableHeight {
			m.configureApplicationTable(values, tableWidth, tableHeight)
		}
		lines := strings.Split(strings.TrimSuffix(m.applicationTable.View(), "\n"), "\n")
		if previewRows > 0 {
			lines = append(lines, "")
			lines = append(lines, m.previewLines(values)...)
		}
		if len(lines) > budget {
			lines = lines[:budget]
		}
		return lines, 3
	}
	view := m.reviewViewport(budget)
	return strings.Split(strings.TrimSuffix(view.View(), "\n"), "\n"), 1
}

func (m model) reviewViewport(height int) viewport.Model {
	view := viewport.New(viewport.WithWidth(max(1, viewWidth(m.width)-1)), viewport.WithHeight(max(1, height)))
	view.SetContent(strings.Join(m.reviewLines(), "\n"))
	view.SetYOffset(m.reviewScroll)
	return view
}

func (m model) workspaceHeight() int {
	// The header, navigation, separator, and contextual help consume three
	// rows plus however many lines the responsive navigation needs.
	feedback := 0
	if m.status != "" {
		feedback++
	}
	if m.err != nil {
		feedback += 2
	}
	navigationRows := strings.Count(m.tabsLine(), "\n") + 1
	return max(3, max(1, m.height)-(3+navigationRows)-feedback)
}

func (m model) hasPreview(values []app.Application) bool {
	return m.isListScreen() && len(values) > 0 && m.workspaceHeight() >= 8
}

func (m model) previewLines(values []app.Application) []string {
	if len(values) == 0 || m.selected < 0 || m.selected >= len(values) {
		return nil
	}
	value := values[m.selected]
	name := m.theme.panel.Render(value.Name)
	description := value.Summary
	if description == "" {
		description = applicationStatus(value)
	}
	transition := value.InstalledVersion
	if value.RegistryVersion != "" && value.RegistryVersion != value.InstalledVersion {
		transition += " → " + value.RegistryVersion
	}
	if value.DefaultChannel != "" {
		transition += " · " + value.DefaultChannel
	}
	return []string{fit(name+" — "+description, max(1, viewWidth(m.width)-2)), fit(transition, max(1, viewWidth(m.width)-2))}
}

func (m *model) configureApplicationTable(values []app.Application, width, height int) {
	// Keep both version identities on compact terminals. Channel is useful
	// metadata, but it yields first when the table cannot fit all columns.
	compact := width < 80
	updatesCompact := compact && m.screen == screenUpdates
	columnCount := 6
	if compact {
		columnCount = 5
	}
	if updatesCompact {
		columnCount = 4
	}
	// Column widths describe content. Bubbles adds the shared cell padding
	// around every column, so reserve exactly that visible overhead here.
	_, paddingRight, _, paddingLeft := applicationTableCellStyle.GetPadding()
	contentWidth := max(1, width-columnCount*(paddingLeft+paddingRight))
	selectionWidth := 1
	if !m.color {
		selectionWidth = 2
	}
	statusWidth := 9 // longest status label is AVAILABLE
	channelWidth := 8
	nameWidth := 1
	versionWidth := 15
	if compact {
		channelWidth = 0
		statusWidth = 8
		versionWidth = 10
	}
	if updatesCompact {
		statusWidth = 0
	}
	versionBudget := max(2, contentWidth-nameWidth-selectionWidth-statusWidth-channelWidth)
	installedWidth := min(versionWidth, max(1, versionBudget/2))
	availableWidth := min(versionWidth, max(1, versionBudget-installedWidth))
	nameWidth = max(1, contentWidth-selectionWidth-statusWidth-installedWidth-availableWidth-channelWidth)
	rows := make([]table.Row, 0, len(values))
	for index, value := range values {
		marker := applicationMarker(m.selectedIDs[value.ID], value.ID == m.cursorID || (m.cursorID == "" && index == m.selected), m.color)
		var status string
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
		if updatesCompact {
			rows = append(rows, table.Row{marker, value.Name, emptyDash(value.InstalledVersion), emptyDash(value.RegistryVersion)})
		} else if compact {
			rows = append(rows, table.Row{marker, value.Name, status, emptyDash(value.InstalledVersion), emptyDash(value.RegistryVersion)})
		} else {
			rows = append(rows, table.Row{marker, value.Name, status, emptyDash(value.InstalledVersion), emptyDash(value.RegistryVersion), emptyDash(channel)})
		}
	}
	if len(rows) == 0 {
		placeholder := "No applications."
		if m.query != "" && (m.screen == screenAvailable || m.screen == screenNotInstalled) {
			placeholder = "No matches for \"" + truncate(m.query, 24, "…") + "\"."
		} else if m.screen == screenInstalled {
			placeholder = "No installed applications."
		} else if m.screen == screenNotInstalled {
			placeholder = "No applications available to install."
		} else if m.screen == screenUpdates {
			placeholder = "No updates available."
		}
		if updatesCompact {
			rows = append(rows, table.Row{"", placeholder, "", ""})
		} else if compact {
			rows = append(rows, table.Row{"", placeholder, "", "", ""})
		} else {
			rows = append(rows, table.Row{"", placeholder, "", "", "", ""})
		}
	}
	columns := []table.Column{
		{Title: "", Width: selectionWidth},
		{Title: "APPLICATION", Width: nameWidth},
	}
	if !updatesCompact {
		columns = append(columns, table.Column{Title: "STATUS", Width: statusWidth})
	}
	columns = append(columns, table.Column{Title: "INSTALLED", Width: installedWidth}, table.Column{Title: "AVAILABLE", Width: availableWidth})
	if !compact {
		columns = append(columns, table.Column{Title: "CHANNEL", Width: channelWidth})
	}
	if len(values) == 0 {
		columns[1].Width = max(1, width-2*(paddingLeft+paddingRight)-selectionWidth)
		for index := 2; index < len(columns); index++ {
			columns[index].Width = 0
		}
	}
	// Bubbles renders while columns change. Clear rows only if the row shape
	// changes; otherwise preserve the table viewport's scroll offset.
	if len(m.applicationTable.Columns()) != len(columns) {
		m.applicationTable.SetRows(nil)
	}
	m.applicationTable.SetColumns(columns)
	m.applicationTable.SetRows(rows)
	if len(values) == 0 {
		if m.applicationTable.Focused() {
			m.applicationTable.Blur()
		}
	} else {
		if !m.applicationTable.Focused() {
			m.applicationTable.Focus()
		}
	}
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
	if m.applicationTable.Cursor() != max(0, cursor) {
		m.applicationTable.SetCursor(max(0, cursor))
	}
	// SetCursor updates the rendered row window but does not always move the
	// viewport offset. A restored cursor at the end of a long list can otherwise
	// sit just below the visible rows after switching back from a short tab.
	m.applicationTable.MoveDown(0)
	m.tableConfigured = true
	m.tableWidth = max(1, width)
	m.tableHeight = max(2, height)
}

// rebuildApplicationTable rebuilds the applications table for the current list
// state. It runs after anything that can change the table rows, markers,
// screen, filter, or workspace budget. Pure cursor navigation never calls it:
// navigation reuses the configured rows so Bubbles keeps the viewport scroll
// offset and no row is rebuilt per keypress.
func (m *model) rebuildApplicationTable() {
	if !m.isListScreen() {
		return
	}
	values := m.visibleApplications()
	width, height, _ := m.applicationTableSize(values, m.listTableBudget())
	m.configureApplicationTable(values, width, height)
}

// applicationTableSize returns the width, height, and preview row count the
// list screen needs for the given workspace budget. Update-time table
// configuration and view-time layout share this single owner so the configured
// table geometry and the rendered layout cannot drift apart.
func (m model) applicationTableSize(values []app.Application, budget int) (width, height, previewRows int) {
	headingRows := 0
	if m.hasPreview(values) && budget >= headingRows+8 {
		previewRows = 3
	}
	tableBudget := max(2, budget-headingRows-previewRows)
	return max(1, viewWidth(m.width)-2), min(tableBudget, max(3, len(values)+2)), previewRows
}

// listTableBudget returns the workspace line budget available to the
// applications table on the active list screen. For list screens it equals the
// budget bodyLines passes to workspaceLinesFor: the shell header, feedback
// lines, and the expanded help overlay are accounted for the same way.
func (m model) listTableBudget() int {
	budget := m.workspaceHeight()
	if m.helpOverlay {
		budget, _ = applyHelpOverlayBudget(budget, m.helpOverlayLines())
	}
	return budget
}

// applyHelpOverlayBudget bounds the expanded help overlay to the workspace
// budget and returns the budget left after the overlay and its separator line.
func applyHelpOverlayBudget(budget int, expanded []string) (int, []string) {
	if len(expanded) > max(1, budget-2) {
		expanded = expanded[:max(1, budget-2)]
	}
	return max(0, budget-len(expanded)-1), expanded
}

func emptyDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func applicationMarker(selected, cursor, color bool) string {
	if color {
		if selected {
			return "✓"
		}
		return " "
	}
	if cursor {
		if selected {
			return ">✓"
		}
		return "> "
	}
	if selected {
		return " ✓"
	}
	return "  "
}

func (m model) reviewLines() []string {
	if m.updatePlan != nil {
		lines := []string{m.theme.panel.Render("Review update plan")}
		for _, entry := range m.updatePlan.Applications {
			lines = append(lines, entry.Name+": "+entry.Status)
			if entry.Target.Version != "" {
				lines = append(lines, fmt.Sprintf("%s [%s] → %s [%s]", entry.Current.Version, entry.Current.Channel, entry.Target.Version, entry.Target.Channel))
			}
			if entry.Reason != "" {
				lines = append(lines, entry.Reason)
			}
			if entry.Status == "update" {
				lines = append(lines, "Package: "+entry.Current.Fingerprint+" → "+entry.Target.Fingerprint)
				if entry.RuntimeChanged {
					lines = append(lines, "Runtime: "+updateRuntimeLabel(entry.Current.Runtime)+" → "+updateRuntimeLabel(entry.Target.Runtime))
				}
				lines = append(lines, "Rollback retained: "+entry.Rollback.Version+" ["+entry.Rollback.Channel+"] "+entry.Rollback.Fingerprint)
			}
			lines = append(lines, "")
		}
		var wrapped []string
		for _, line := range lines {
			if line == "" {
				wrapped = append(wrapped, "")
				continue
			}
			wrapped = append(wrapped, wrappedLines(line, max(1, viewWidth(m.width)-2))...)
		}
		return wrapped
	}
	var lines []string
	// The tab bar stays in the shell on every screen, so the detail context
	// moves into the workspace instead of replacing navigation.
	if breadcrumb := m.breadcrumb(); breadcrumb != "" {
		lines = append(lines, m.theme.muted.Render(fit(breadcrumb, max(1, viewWidth(m.width)-2))), "")
	}
	if len(m.selectedIDs) > 0 {
		lines = append(lines, m.theme.panel.Render("Review selection"), fmt.Sprintf("%d applications selected", len(m.selectedIDs)))
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
		lines = append(lines, "")
		return lines
	}
	if m.detail == nil {
		return []string{"Select an application to review."}
	}
	if m.screen == screenVersions {
		lines = append(lines, m.theme.panel.Render(detailName(m.detail)), "Version history")
		if len(m.versions) == 0 {
			return append(lines, "", "No versions available.")
		}
		showChannel := false
		for _, value := range m.versions {
			showChannel = showChannel || value.Channel != ""
		}
		lines = append(lines, "", versionHeadingColumns(viewWidth(m.width)-1, showChannel))
		for _, value := range m.versions {
			lines = append(lines, versionRowColumns(value, viewWidth(m.width)-1, showChannel))
		}
		return lines
	}
	lines = append(lines, m.theme.panel.Render(m.detail.Name))
	state := applicationStatus(*m.detail)
	if m.detail.UpdateAvailable && !m.detail.Pinned {
		state = "Update"
	}
	if state != "" {
		available := max(1, viewWidth(m.width)-2)
		stateStyle := m.theme.success
		if m.detail.UpdateAvailable && !m.detail.Pinned {
			stateStyle = m.theme.warning
		}
		if m.detail.InstalledVersion == "" {
			stateStyle = m.theme.muted
		}
		stateText := stateStyle.Render(state)
		stateWidth := displaywidth.Options{ControlSequences: true}.String(stateText)
		lines[0] = lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(max(1, available-stateWidth)).Render(truncate(lines[0], max(1, available-stateWidth), "…")), stateText)
	}
	lines = append(lines, wrappedLines(m.detail.Summary, max(1, viewWidth(m.width)-2))...)
	if m.feedbackAppID == m.detail.ID && m.feedbackAppID != "" {
		if m.status != "" {
			tone := success
			if m.feedbackPartial {
				tone = warning
			}
			lines = append(lines, m.style(m.status, tone))
		}
		if m.err != nil {
			lines = append(lines, m.style("Operation failed", danger))
			lines = append(lines, wrappedLines(m.err.Error(), max(1, viewWidth(m.width)-2))...)
		}
	}
	lines = append(lines, "")
	addDetailFields(&lines, *m.detail, viewWidth(m.width)-2, m.theme)
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
	// Modal controls are rendered by the shell footer. Keeping one command
	// surface prevents the overlay and footer from drifting apart.
	return lines
}

func (m model) modalContent(content []string, width int) (int, []string) {
	available := max(1, width-8)
	contentWidth := min(72, available)
	for _, line := range content {
		contentWidth = max(contentWidth, displaywidth.Options{ControlSequences: true}.String(line))
	}
	contentWidth = min(contentWidth, available)
	bounded := make([]string, 0, len(content))
	for _, line := range content {
		if line == "" {
			bounded = append(bounded, "")
			continue
		}
		bounded = append(bounded, wrappedLines(line, contentWidth)...)
	}
	return contentWidth, bounded
}

func (m model) modalViewport(height int) viewport.Model {
	width, lines := m.modalContent(m.overlayLines(), viewWidth(m.width))
	v := viewport.New(viewport.WithWidth(width), viewport.WithHeight(max(1, height)))
	v.SetContent(strings.Join(lines, "\n"))
	v.SetYOffset(m.overlayScroll)
	return v
}

func (m model) helpOverlayLines() []string {
	bindings := m.actionBindings()
	m.help.SetWidth(max(1, viewWidth(m.width)-4))
	return append([]string{m.theme.panel.Render("Keyboard reference")}, strings.Split(m.help.FullHelpView([][]keypkg.Binding{bindings}), "\n")...)
}

func (m model) headerLine() string {
	left := m.theme.panel.Render("TarLink " + version.Current)
	right := ""
	if m.upgradeAvailable {
		right = m.theme.warning.Render("TarLink update available")
	}
	if right == "" {
		return fit(left, m.width)
	}
	measure := displaywidth.Options{ControlSequences: true}
	if viewWidth(m.width) < measure.String(left)+measure.String(right)+3 {
		return fit(left, m.width)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(viewWidth(m.width)-measure.String(right)).Render(left), right)
}

// navigationScreen returns the top-level tab that owns the current screen.
// Detail, version, and confirmation screens keep the tab that opened them
// marked so the top navigation never disappears while they are active.
func (m model) navigationScreen() screen {
	if m.isListScreen() {
		return m.screen
	}
	switch m.returnTo {
	case screenInstalled, screenNotInstalled, screenUpdates:
		return m.returnTo
	}
	return screenAvailable
}

func (m model) tabsLine() string {
	if m.searching {
		return fit("Search  "+m.searchInput.View(), m.width)
	}
	installedCount, updatesCount := "", ""
	if m.dataLoaded {
		installedCount = fmt.Sprintf(" %d", len(m.installed))
		updatesCount = fmt.Sprintf(" %d", len(updates(m.installed)))
	}
	labels := []struct {
		screen screen
		label  string
	}{
		{screenAvailable, "Browse"},
		{screenInstalled, "Installed" + installedCount},
		{screenNotInstalled, "Not installed"},
		{screenUpdates, "Updates" + updatesCount},
	}
	parts := make([]string, len(labels))
	active := m.navigationScreen()
	for index, tab := range labels {
		parts[index] = m.navigationLabel(tab.label, active == tab.screen)
	}
	var rows []string
	row := ""
	for _, part := range parts {
		candidate := part
		if row != "" {
			candidate = row + "   " + part
		}
		if row != "" && (displaywidth.Options{ControlSequences: true}).String(candidate) > viewWidth(m.width) {
			rows = append(rows, row)
			row = part
		} else {
			row = candidate
		}
	}
	if row != "" {
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

// navigationLabel renders one top-bar tab with its keyboard mnemonic
// underlined. The mnemonic stays underlined on inactive tabs so the B/I/N/U
// shortcuts remain discoverable without opening Help.
func (m model) navigationLabel(label string, active bool) string {
	style := m.theme.control
	if active {
		style = m.theme.controlSelected
	}
	var rendered string
	if label != "" {
		rendered = style.Underline(true).Render(label[:1]) + style.Render(label[1:])
	} else {
		rendered = style.Render(label)
	}
	if active {
		return style.Render("[ ") + rendered + style.Render(" ]")
	}
	// The active tab wraps its label in brackets. Reserve the same two columns
	// on each side for inactive tabs so neither the label nor its neighbors
	// shift when the selection moves between tabs.
	return lipgloss.NewStyle().Padding(0, 2).Render(rendered)
}
func (m model) separator() string { return strings.Repeat("─", viewWidth(m.width)) }

// breadcrumb locates the detail workspace inside the active top-level tab.
// It is rendered in the workspace because the shell tab bar stays constant.
func (m model) breadcrumb() string {
	if m.detail == nil || (m.screen != screenDetails && m.screen != screenVersions) {
		return ""
	}
	root := "Browse"
	switch m.returnTo {
	case screenInstalled:
		root = "Installed"
	case screenNotInstalled:
		root = "Not installed"
	case screenUpdates:
		root = "Updates"
	}
	value := root + " › " + detailName(m.detail)
	if m.screen == screenVersions {
		value += " › Versions"
	}
	return value
}

func detailName(value *app.Application) string {
	if value == nil {
		return ""
	}
	return value.Name
}

func (m model) isListScreen() bool {
	return m.screen == screenAvailable || m.screen == screenInstalled || m.screen == screenNotInstalled || m.screen == screenUpdates
}

func (m model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	m.initComponents()
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
	if m.searching {
		switch {
		case keypkg.Matches(message, bindings.Cancel):
			if !m.matchesAction(message, actionCancel) {
				return m, nil
			}
			m.searching = false
			m.searchInput.Blur()
			m.screen = m.searchFrom
			m.query = m.searchPriorQuery
			m.selected = m.searchPriorSelected
			m.cursorID = m.searchPriorCursorID
			m.clampSelection()
			return m, nil
		case keypkg.Matches(message, bindings.Enter):
			if !m.matchesAction(message, actionEnter) {
				return m, nil
			}
			m.searching = false
			m.query = m.searchInput.Value()
			m.busy = "Searching"
			m.requestGeneration++
			cmd, cancel := m.searchCmd()
			m.opCancel = cancel
			return m, cmd
		default:
			var cmd tea.Cmd
			m.searchInput, cmd = m.searchInput.Update(message)
			return m, cmd
		}
	}
	if m.matchesAction(message, actionHelp) {
		m.helpOverlay = !m.helpOverlay
		return m, nil
	}
	if m.helpOverlay {
		if keypkg.Matches(message, bindings.Cancel) {
			m.helpOverlay = false
		}
		return m, nil
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
	if m.matchesAction(message, actionToggle) {
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
		if m.screen == screenDetails || m.screen == screenVersions {
			m.moveReviewScroll(delta)
		} else if m.screen == screenInstallChannel {
			m.moveChannel(delta)
		} else if m.isOverlay() {
			m.moveOverlayScroll(delta)
		} else {
			m.moveSelection(delta)
		}
	case m.matchesAction(message, actionDown):
		m.clearFeedback()
		if m.screen == screenDetails || m.screen == screenVersions {
			m.moveReviewScroll(1)
		} else if m.screen == screenInstallChannel {
			m.moveChannel(1)
		} else if m.isOverlay() {
			m.moveOverlayScroll(1)
		} else {
			m.moveSelection(1)
		}
	case m.matchesAction(message, actionSearch):
		m.clearFeedback()
		m.searchFrom = m.screen
		m.searchPriorQuery = m.query
		m.searchPriorSelected = m.selected
		m.searchPriorCursorID = m.cursorID
		m.screen = screenAvailable
		m.searching = true
		m.searchInput.SetValue(m.query)
		m.searchInput.Focus()
	case m.matchesAction(message, actionTabs):
		delta := -1
		if keypkg.Matches(message, bindings.Right) {
			delta = 1
		}
		m.switchAdjacentTab(delta)
	case m.matchesAction(message, actionBrowse):
		m.switchTab(screenAvailable)
	case m.matchesAction(message, actionInstalled):
		m.switchTab(screenInstalled)
	case m.matchesAction(message, actionNotInstalled):
		m.switchTab(screenNotInstalled)
	case m.matchesAction(message, actionUpdates):
		m.switchTab(screenUpdates)
	case m.matchesAction(message, actionCancel):
		m.updatePlan = nil
		m.clearFeedback()
		if m.screen == screenUninstall {
			m.uninstallConflict = nil
		}
		m.reviewScroll = 0
		m.overlayScroll = 0
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
				m.requestGeneration++
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
				m.screen = screenDetails
				m.startOperation()
				cmd, cancel := m.batchInstallCmd(m.batchIDs)
				m.opCancel = cancel
				return m, cmd
			}
			if id := m.selectedID(); id != "" {
				m.busy = "Installing " + id
				m.screen = screenDetails
				m.startOperation()
				cmd, cancel := m.installCmd(m.installSelector(id))
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenDetails {
			if m.updatePlan != nil {
				plan := *m.updatePlan
				m.updatePlan = nil
				m.busy = "Applying update plan"
				m.startOperation()
				cmd, cancel := m.applyUpdatePlanCmd(plan)
				m.opCancel = cancel
				return m, cmd
			}
			if len(m.selectedIDs) > 0 {
				if m.screen == screenDetails && (m.returnTo == screenAvailable || m.returnTo == screenNotInstalled) {
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
			m.reviewScroll = 0
			m.screen = screenDetails
			return m, nil
		}
	case m.matchesAction(message, actionVersions):
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.clearFeedback()
			m.reviewScroll = 0
			m.setDetail(id)
			if m.screen == screenDetails {
				m.versionsFromDetails = true
			} else if m.screen != screenVersions {
				m.returnTo = m.screen
				m.versionsFromDetails = false
			}
			m.busy = "Loading versions"
			m.requestGeneration++
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
	if !m.cursorMovement(message) {
		m.clampSelection()
	}
	return m, nil
}

// cursorMovement reports whether the key is a pure list cursor movement, which
// reuses the configured table rows instead of rebuilding them.
func (m model) cursorMovement(message tea.KeyPressMsg) bool {
	return m.matchesAction(message, actionUp) || m.matchesAction(message, actionDown)
}

func (m *model) moveSelection(delta int) {
	values := m.visibleApplications()
	if len(values) == 0 {
		m.selected = 0
		return
	}
	previous := m.applicationTable.Cursor()
	count := len(values)
	target := previous
	if delta < 0 {
		if previous <= 0 {
			// Wrap from the first row to the last.
			target = count - 1
		} else {
			target = previous - 1
		}
	} else {
		if previous >= count-1 {
			// Wrap from the last row to the first.
			target = 0
		} else {
			target = previous + 1
		}
	}
	if target == previous {
		return
	}
	switch {
	case delta < 0 && target > previous:
		m.applicationTable.GotoBottom()
	case delta > 0 && target < previous:
		m.applicationTable.GotoTop()
	case delta < 0:
		m.applicationTable.MoveUp(previous - target)
	default:
		m.applicationTable.MoveDown(target - previous)
	}
	m.selected = m.applicationTable.Cursor()
	if m.selected >= 0 && m.selected < len(values) {
		m.cursorID = values[m.selected].ID
	}
	if !m.color && previous != m.selected {
		rows := m.applicationTable.Rows()
		for _, index := range []int{previous, m.selected} {
			if index >= 0 && index < len(values) && index < len(rows) {
				rows[index][0] = applicationMarker(m.selectedIDs[values[index].ID], index == m.selected, false)
			}
		}
		m.applicationTable.SetRows(rows)
	}
}

func (m *model) moveReviewScroll(delta int) {
	v := m.reviewViewport(max(1, m.height-4))
	if delta < 0 {
		v.ScrollUp(-delta)
	} else {
		v.ScrollDown(delta)
	}
	m.reviewScroll = v.YOffset()
}

func (m model) modalScrollable() bool {
	if !m.isOverlay() || m.screen == screenInstallChannel {
		return false
	}
	_, lines := m.modalContent(m.overlayLines(), viewWidth(m.width))
	return len(lines) > max(1, m.height-8)
}

func (m *model) moveOverlayScroll(delta int) {
	v := m.modalViewport(max(1, m.height-8))
	if delta < 0 {
		v.ScrollUp(-delta)
	} else {
		v.ScrollDown(delta)
	}
	m.overlayScroll = v.YOffset()
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
	v := m.modalViewport(max(1, m.height-8))
	v.EnsureVisible(3+m.channelSelected, 0, 1)
	m.overlayScroll = v.YOffset()
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
		m.requestGeneration++
		cmd, cancel := m.pathCheckCmd(id)
		m.opCancel = cancel
		return m, cmd
	case m.detail.Pinned:
		m.clearFeedback()
		m.status = id + " is pinned at " + m.detail.InstalledVersion
		return m, nil
	case m.detail.UpdateAvailable:
		planner, ok := m.service.(app.UpdatePlanService)
		if !ok {
			m.err = errors.New("update planning is unavailable")
			return m, nil
		}
		m.clearFeedback()
		m.busy = "Planning update for " + id
		cmd, cancel := m.cancellableCmd(func(ctx context.Context) tea.Msg {
			plan, err := planner.PlanUpdates(ctx, []string{id})
			return updatePlanMsg{plan: plan, err: err}
		}, updatePlanMsg{cancelled: true})
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
	m.screen = screenDetails
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
	m.requestGeneration++
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
	generation := m.requestGeneration
	return func() tea.Msg {
		available, err := m.service.Search(m.ctx, m.query)
		if err != nil {
			return loadedMsg{err: err, generation: generation}
		}
		installed, err := m.service.List(m.ctx)
		return loadedMsg{available: available, installed: installed, err: err, generation: generation}
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
	feedbackID := id
	if m.detail != nil {
		feedbackID = m.detail.ID
	}
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.Install(ctx, id, sink)
		return operationMsg{message: resultMessage("Installed", result), appID: feedbackID}, err
	})
}

func (m model) batchInstallCmd(ids []string) (tea.Cmd, context.CancelFunc) {
	service := m.service.(app.BatchService)
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := service.InstallBatch(ctx, ids, sink)
		return operationMsg{message: batchMessage("Installed", result), partial: len(result.Failed) > 0, move: true, next: screenAvailable}, err
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

func updateRuntimeLabel(value *app.UpdateRuntime) string {
	if value == nil {
		return "self-contained"
	}
	return value.ID + "@" + value.Version + " (" + value.Fingerprint + ")"
}

func (m model) applyUpdatePlanCmd(plan app.UpdatePlan) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.(app.UpdatePlanService).ApplyUpdatePlan(ctx, plan, sink)
		message := fmt.Sprintf("Updated %d; skipped %d; failed %d", len(result.Updated), len(result.Skipped), len(result.Failed))
		if err == nil && len(result.Failed) > 0 {
			ids := make([]string, 0, len(result.Failed))
			for id := range result.Failed {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			err = errors.New(result.Failed[ids[0]])
		}
		feedbackID := ""
		if len(plan.Applications) == 1 {
			feedbackID = plan.Applications[0].ID
		}
		return operationMsg{message: message, appID: feedbackID, partial: len(result.Failed) > 0}, err
	})
}

func (m model) rollbackCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		result, err := m.service.Rollback(ctx, id, sink)
		return operationMsg{message: resultMessage("Rolled back", result), appID: id, next: screenDetails, move: true}, err
	})
}

func (m model) operationCmd(operation func(context.Context, app.ProgressSink) (operationMsg, error)) (tea.Cmd, context.CancelFunc) {
	base := m.ctx
	if base == nil {
		base = context.Background()
	}
	opCtx, opCancel := context.WithCancel(base)
	generation := m.operationGeneration
	hub := &operationHub{wake: make(chan struct{}, 1), result: make(chan operationMsg, 1), ctx: opCtx, generation: generation}
	cmd := func() tea.Msg {
		go func() {
			sink := func(event app.Progress) {
				hub.publish(event)
			}
			result, err := operation(opCtx, sink)
			result.err = err
			result.generation = generation
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
			return progressMsg{hub: h, event: event, generation: h.generation}
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
						return progressMsg{hub: h, event: event, generation: h.generation}
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
					return operationMsg{err: ctx.Err(), generation: h.generation}
				}
				continue
			}
			event := *h.latest
			h.latest = nil
			h.lastEmit = time.Now()
			h.mu.Unlock()
			return progressMsg{hub: h, event: event, generation: h.generation}
		}
		h.mu.Unlock()
		select {
		case result := <-h.result:
			return result
		case <-h.wake:
		case <-ctx.Done():
			return operationMsg{err: ctx.Err(), generation: h.generation}
		}
	}
}

func (m model) uninstallCmd(id string) (tea.Cmd, context.CancelFunc) {
	return m.operationCmd(func(ctx context.Context, sink app.ProgressSink) (operationMsg, error) {
		_, err := m.service.Uninstall(ctx, id, sink)
		next := m.confirmationTarget()
		if next == screenVersions {
			next = screenDetails
		}
		result := operationMsg{message: "Uninstalled " + id, appID: id, next: next, move: true}
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
		_, err := m.service.Uninstall(ctx, conflict.AppID, sink)
		next := m.confirmationTarget()
		if next == screenVersions {
			next = screenDetails
		}
		result := operationMsg{message: "Uninstalled " + conflict.AppID, appID: conflict.AppID, next: next, move: true}
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
		return operationMsg{message: batchMessage("Uninstalled", result), partial: len(result.Failed) > 0, move: true, next: screenInstalled}, err
	})
}

func batchMessage(verb string, result app.BatchResult) string {
	message := fmt.Sprintf("%s %d applications", verb, len(result.Completed))
	if len(result.Failed) > 0 {
		message += fmt.Sprintf("; %d failed", len(result.Failed))
		ids := make([]string, 0, len(result.Failed))
		for id := range result.Failed {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			message += "\n" + id + ": " + result.Failed[id]
		}
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
	case screenNotInstalled:
		visible := make([]app.Application, 0, len(m.available))
		for _, value := range m.available {
			if value.InstalledVersion == "" {
				visible = append(visible, value)
			}
		}
		return visible
	case screenUpdates:
		return updates(m.installed)
	}
	return m.available
}

// switchTab moves the list workspace to another top-level tab. It keeps the
// stable cursor ID when the target list contains it and clamps the previous
// index otherwise. Batch selections and any stale detail belong to the old
// list and are cleared.
func (m *model) switchTab(target screen) {
	m.updatePlan = nil
	if m.screen == target {
		return
	}
	m.clearFeedback()
	m.selectedIDs = nil
	m.batchIDs, m.batchTargets = nil, nil
	m.detail = nil
	m.screen = target
	m.clampSelection()
}

func (m *model) switchAdjacentTab(delta int) {
	tabs := [...]screen{screenAvailable, screenInstalled, screenNotInstalled, screenUpdates}
	index := 0
	for candidate, target := range tabs {
		if target == m.screen {
			index = candidate
			break
		}
	}
	m.switchTab(tabs[(index+delta+len(tabs))%len(tabs)])
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
	values := m.visibleApplications()
	if len(values) == 0 {
		m.selected = 0
	} else {
		if m.cursorID != "" {
			for index, value := range values {
				if value.ID == m.cursorID {
					m.selected = index
					break
				}
			}
		}
		if m.selected >= len(values) {
			m.selected = len(values) - 1
		}
	}
	if m.selected >= 0 && m.selected < len(values) {
		m.cursorID = values[m.selected].ID
	}
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
		if width < 35 {
			*lines = append(*lines, theme.muted.Render(label))
			*lines = append(*lines, wrappedLines(content, max(1, width))...)
			return
		}
		chunks := wrappedLines(content, max(1, width-14))
		for i, chunk := range chunks {
			caption := ""
			if i == 0 {
				caption = label
			}
			*lines = append(*lines, lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(14).Render(theme.muted.Render(caption)), chunk))
		}
	}
	channel := value.InstalledChannel
	if channel == "" {
		channel = value.DefaultChannel
	}
	if value.InstalledVersion != "" || value.RegistryVersion != "" || channel != "" {
		*lines = append(*lines, theme.panel.Render("VERSION"))
		field("Installed", value.InstalledVersion)
		field("Available", value.RegistryVersion)
		field("Channel", channel)
	}
	if len(value.Categories) > 0 || hasGameData(value) || value.ID != "" {
		*lines = append(*lines, "", theme.panel.Render("PACKAGE"))
		field("Categories", strings.Join(value.Categories, ", "))
		if hasGameData(value) {
			field("Requires", "Original game data")
		}
		field("ID", value.ID)
	}
	if value.Homepage != "" {
		*lines = append(*lines, "", theme.panel.Render("HOMEPAGE"))
		*lines = append(*lines, wrappedLines(value.Homepage, max(1, width))...)
	}
}

func wrappedLines(value string, width int) []string {
	if value == "" {
		return nil
	}
	return strings.Split(ansi.Hardwrap(ansi.Wrap(value, width, ""), width, false), "\n")
}

func versionHeadingColumns(width int, channel bool) string {
	if viewWidth(width) < 40 {
		return "VERSION"
	}
	if channel && width >= 70 {
		third := max(1, width/3)
		return lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(third).Render("VERSION"),
			lipgloss.NewStyle().Width(third).Render("CHANNEL"),
			lipgloss.NewStyle().Width(max(1, width-2*third)).Render("STATE"))
	}
	half := max(1, viewWidth(width)/2)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(half).Render("VERSION"),
		lipgloss.NewStyle().Width(half).Render("STATUS"))
}
func versionRowColumns(value app.Version, width int, channel bool) string {
	if viewWidth(width) < 40 {
		return truncate(value.Version, width, "…")
	}
	if channel && width >= 70 {
		third := max(1, width/3)
		return lipgloss.JoinHorizontal(lipgloss.Top,
			lipgloss.NewStyle().Width(third).Render(truncate(value.Version, third, "…")),
			lipgloss.NewStyle().Width(third).Render(truncate(value.Channel, third, "…")),
			lipgloss.NewStyle().Width(max(1, width-2*third)).Render(truncate(value.Status, max(1, width-2*third), "…")))
	}
	half := max(1, viewWidth(width)/2)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(half).Render(truncate(value.Version, half, "…")),
		lipgloss.NewStyle().Width(half).Render(truncate(value.Status, half, "…")))
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
	if m.progress.Stage == "" && m.progress.Description == "" && m.progress.AppID == "" && m.progress.BytesDone == 0 && m.progress.BytesTotal == 0 {
		return ""
	}
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
	} else if m.progress.AppID != "" {
		line = strings.TrimSpace(stage + " · " + m.progress.AppID)
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
	m.feedbackAppID = ""
	m.feedbackPartial = false
	m.err = nil
}

// startOperation resets progress and clears stale feedback before a new
// active operation begins, so a previous operation's success/error never
// leaks into the next one.
func (m *model) startOperation() {
	m.operationGeneration++
	m.requestGeneration++
	m.overlayScroll = 0
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
