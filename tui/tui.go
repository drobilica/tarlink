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

type paneFocus uint8

const (
	focusList paneFocus = iota
	focusDetail
	focusSearch
	focusOverlay
)

type versionCheckState uint8

const (
	versionChecking versionCheckState = iota
	versionCurrent
	versionAvailable
	versionUnavailable
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
	selectedIDs         map[string]bool
	batchIDs            []string
	batchTargets        []app.BatchTarget
	batchUninstall      bool
	channelSelected     int
	channels            []string
	listOffset          int
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
	versionState        versionCheckState
	versionCheckError   error
	cancel              context.CancelFunc
	opCancel            context.CancelFunc
	uninstallConflict   *app.UninstallConflict
	focus               paneFocus
	previousFocus       paneFocus
	helpOverlay         bool
	componentsReady     bool
	searchInput         textinput.Model
	detailViewport      viewport.Model
	versionsViewport    viewport.Model
}

const (
	chromeHeight   = 3 // header, tabs, separator
	activityHeight = 2
	footerHeight   = 1
)

func (m model) workspaceHeight() int {
	return m.layout().Workspace.height
}

// Run starts the terminal renderer. All application changes are delegated to
// the same service API used by the CLI.
func Run(ctx context.Context, service app.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return fmt.Errorf("TarLink core is unavailable")
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	m := model{ctx: operationContext, service: service, screen: screenAvailable, color: colorEnabled(output), theme: newTheme(colorEnabled(output)), help: newHelp(colorEnabled(output)), progressBar: newProgress(colorEnabled(output)), cancel: cancel, versionState: versionChecking}
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
		bounds := m.layout().Applications
		m.configureApplicationTable(m.visibleApplications(), max(1, bounds.width-2), max(1, bounds.height-2))
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampSelection()
		m.help.SetWidth(m.width)
		m.progressBar.SetWidth(progressBarWidthFor(m.width))
		m.searchInput.SetWidth(max(12, m.layout().Applications.width-8))
		m.setViewportSize()
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
		if message.err != nil {
			m.versionState = versionUnavailable
			m.versionCheckError = message.err
		} else {
			m.tarlinkVersion = message.value
			m.upgradeAvailable = message.value.UpgradeAvailable
			m.versionCheckError = nil
			if message.value.UpgradeAvailable {
				m.versionState = versionAvailable
			} else {
				m.versionState = versionCurrent
			}
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
			m.openOverlay(screenInstallConfirm)
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
		m.openOverlay(screenInstallConfirm)
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
		m.listOffset = 0
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
			m.versionState = versionCurrent
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
	case tea.MouseClickMsg:
		return m.updateMouse(message)
	case tea.MouseWheelMsg:
		return m.updateMouse(message)
	}
	return m, nil
}

func (m model) View() tea.View {
	m.initComponents()
	m.setViewportSize()
	base := m.bodyLines()
	if modal := m.modalLayer(); modal != nil {
		base = m.composeLayer(base, modal)
	}
	if m.helpOverlay {
		base = m.composeLayer(base, m.helpLayer())
	}
	// Assign the already normalized frame directly. NewView/SetContent applies
	// styled-string line normalization, which can expand block-composed ANSI
	// rows and violates the shell's fixed geometry contract.
	view := tea.NewView("")
	view.Content = strings.Join(base, "\n") + "\n"
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// bodyLines is the shared shell and screen content. Keeping chrome here makes
// the footer and mouse coordinates agree with every screen's rendered layout.
func (m model) bodyLines() []string {
	lines := []string{m.headerLine(), m.tabsLine(), m.separator()}
	workspace, _ := m.workspaceLines()
	lines = append(lines, workspace...)
	activity := []string{"", ""}
	if m.busy != "" {
		activity[0] = m.style(m.busy, accent)
		if m.progress.Stage != "" {
			activity[1] = m.progressLine()
		}
	} else if m.err != nil {
		activity[0] = m.style("Operation failed", danger)
		activity[1] = m.style(m.err.Error(), danger)
		if m.uninstallConflict != nil {
			activity[1] = m.style("Conflicting integration: "+m.uninstallConflict.Path, danger)
		}
	} else if m.status != "" {
		activity[0] = m.style(m.status, success)
	} else if m.uninstallConflict != nil {
		activity[0] = fit("Conflicting integration: "+m.uninstallConflict.Path, m.width)
	} else if m.upgradeAvailable {
		activity[0] = m.style("TarLink update available: "+m.tarlinkVersion.Current+" → "+m.tarlinkVersion.Latest, warning)
	}
	for i := range activity {
		lines = append(lines, fit(activity[i], m.width))
	}
	lines = append(lines, m.style(fit(m.formattedFooter(), viewWidth(m.width)), muted))
	return padLines(lines, viewWidth(m.width), max(m.height, len(lines)))
}

func padLines(lines []string, width, height int) []string {
	result := make([]string, height)
	for i := range result {
		if i < len(lines) {
			result[i] = fit(lines[i], width)
		} else {
			result[i] = ""
		}
	}
	return result
}

func (m model) composeLayer(base []string, overlay *lipgloss.Layer) []string {
	width, height := viewWidth(m.width), max(m.height, len(base))
	root := lipgloss.NewLayer(strings.Join(padLines(base, width, height), "\n"))
	compositor := lipgloss.NewCompositor(root, overlay)
	return padLines(strings.Split(compositor.Render(), "\n"), width, height)
}

func (m model) modalLayer() *lipgloss.Layer {
	if !m.isOverlay() {
		return nil
	}
	width := min(64, max(10, m.layout().Width-4))
	return m.centeredOverlay(m.overlayContent(width-4), width, "modal")
}

func (m model) helpLayer() *lipgloss.Layer {
	width := min(72, max(10, m.layout().Width-4))
	return m.centeredOverlay(m.helpOverlayLines(), width, "help")
}

func (m model) centeredOverlay(content []string, width int, id string) *lipgloss.Layer {
	width = min(width, max(1, m.layout().Width))
	inner := max(1, width-2)
	lines := make([]string, len(content))
	for i, line := range content {
		lines[i] = fit(line, inner)
	}
	box := lipgloss.NewStyle().Width(inner).Border(lipgloss.RoundedBorder()).Padding(0, 1).Render(strings.Join(lines, "\n"))
	boxWidth := 0
	boxLines := strings.Split(box, "\n")
	for _, line := range boxLines {
		boxWidth = max(boxWidth, displaywidth.String(line))
	}
	boxHeight := len(boxLines)
	l := m.layout()
	x := max(0, (l.Width-boxWidth)/2)
	y := max(0, (l.Height-boxHeight)/2)
	return lipgloss.NewLayer(box).ID(id).X(x).Y(y).Z(10)
}

func (m *model) initComponents() {
	if m.componentsReady {
		return
	}
	m.searchInput = textinput.New()
	m.searchInput.Prompt = ""
	m.searchInput.Placeholder = "Search applications"
	m.searchInput.CharLimit = 128
	m.detailViewport = viewport.New()
	m.detailViewport.SoftWrap = true
	m.versionsViewport = viewport.New()
	m.versionsViewport.SoftWrap = true
	m.applicationTable = table.New(table.WithFocused(true))
	m.applicationTable.SetStyles(table.Styles{
		Header:   m.theme.panel,
		Cell:     lipgloss.NewStyle().PaddingRight(1),
		Selected: m.theme.selected,
	})
	m.focus = focusList
	m.componentsReady = true
}

func (m *model) setViewportSize() {
	l := m.layout()
	m.detailViewport.SetWidth(max(1, l.Details.width-4))
	m.detailViewport.SetHeight(max(1, l.Details.height-2))
	m.versionsViewport.SetWidth(max(1, l.Details.width-4))
	m.versionsViewport.SetHeight(max(1, l.Details.height-2))
}

func (m *model) setFocus(focus paneFocus) {
	m.focus = focus
	switch focus {
	case focusList:
		m.applicationTable.Focus()
		m.searchInput.Blur()
	case focusDetail:
		m.applicationTable.Blur()
		m.searchInput.Blur()
	case focusSearch:
		m.applicationTable.Blur()
		m.searchInput.Focus()
	default:
		m.applicationTable.Blur()
		m.searchInput.Blur()
	}
}

func (m *model) openOverlay(screen screen) {
	m.previousFocus = m.focus
	m.screen = screen
	m.setFocus(focusOverlay)
}

func (m *model) closeOverlay(target screen) {
	m.screen = target
	if m.previousFocus != focusList && m.previousFocus != focusDetail {
		m.previousFocus = focusList
	}
	m.setFocus(m.previousFocus)
}

func (m model) isOverlay() bool {
	return m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenInstallChannel || m.screen == screenUninstallConflictConfirm
}

func (m model) workspaceLines() ([]string, int) {
	// Resolve the covered route locally for rendering. The model remains in
	// overlay state, so actions and input still belong exclusively to the
	// overlay while the underlying workspace stays stable.
	if m.isOverlay() {
		m.screen = m.confirmationTarget()
	}
	l := m.layout()
	if l.Workspace.height == 0 {
		return nil, 0
	}
	values := m.visibleApplications()
	if m.detail == nil && len(values) > 0 && m.selected < len(values) {
		value := values[m.selected]
		m.detail = &value
	}
	if m.isListScreen() {
		m.configureApplicationTable(values, max(1, l.Applications.width-2), max(1, l.Applications.height-2))
	}
	list := m.applicationPanel(l.Applications)
	if l.Mode == modeNarrow && !m.isListScreen() {
		return strings.Split(m.panelBlock(l.Details, m.detailPanelContent(l.Details), m.focus == focusDetail), "\n"), 0
	}
	if l.Mode == modeNarrow {
		return strings.Split(list, "\n"), 1
	}
	detail := m.panelBlock(l.Details, m.detailPanelContent(l.Details), m.focus == focusDetail)
	return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, list, detail), "\n"), 1
}

func (m model) applicationPanel(bounds rect) string {
	content := []string{m.panelTitle("Applications", m.focus == focusList)}
	if m.screen == screenAvailable {
		content = append(content, m.filterView())
	}
	if m.searching {
		content = append(content, m.searchInput.View())
	}
	content = append(content, strings.Split(m.applicationTable.View(), "\n")...)
	return m.panelBlock(bounds, content, m.focus == focusList)
}

func (m model) detailPanelContent(bounds rect) []string {
	content := []string{m.panelTitle("Details · Selected application", m.focus == focusDetail)}
	detail, _ := m.detailLines()
	content = append(content, detail...)
	return content
}

func (m model) panelTitle(label string, active bool) string {
	if active {
		return "▶ " + label
	}
	return "  " + label
}

func (m model) panelBlock(bounds rect, content []string, active bool) string {
	if bounds.height <= 0 || bounds.width <= 0 {
		return ""
	}
	innerWidth := max(1, bounds.width-2)
	innerHeight := max(0, bounds.height-2)
	lines := make([]string, 0, innerHeight)
	if innerHeight > 0 {
		for _, line := range content {
			lines = append(lines, fit(line, innerWidth))
			if len(lines) == innerHeight {
				break
			}
		}
	}
	lines = padLines(lines, innerWidth, innerHeight)
	for i, line := range lines {
		if line == "" {
			lines[i] = strings.Repeat(" ", innerWidth)
		}
	}
	// The content is already normalized to the assigned height. Width is the
	// only dimension Lip Gloss needs here; setting Height would add another
	// minimum-height region around the already padded block.
	panelBorder := lipgloss.Border{
		Top: "─", Bottom: "─", Left: "│", Right: "│",
		TopLeft: "╓", TopRight: "╖", BottomLeft: "╙", BottomRight: "╜",
	}
	style := lipgloss.NewStyle().Width(innerWidth).Height(innerHeight).Border(panelBorder)
	rendered := style.Render(strings.Join(lines, "\n"))
	// Border rendering can add rows at the edges. Normalize the final block
	// back to its assigned rectangle so the shell cannot grow at small sizes.
	return strings.Join(padLines(strings.Split(rendered, "\n"), bounds.width, bounds.height), "\n")
}

// workspaceLinesLegacy is retained only as a comparison point while the
// layout contract is exercised by the renderer; it is not called.
func (m model) workspaceLinesLegacy() ([]string, int) {
	// A modal is transient state: render the page it covers unchanged.
	if m.isOverlay() {
		target := m.confirmationTarget()
		if m.screen == screenInstallChannel && target == screenAvailable && m.detail != nil {
			target = screenDetails
		}
		m.screen = target
	}
	values := m.visibleApplications()
	if m.detail == nil && len(values) > 0 && m.selected < len(values) {
		value := values[m.selected]
		m.detail = &value
	}
	leftWidth := max(28, (viewWidth(m.width)*46)/100)
	wide := viewWidth(m.width) >= 72
	if m.isListScreen() {
		m.configureApplicationTable(values, leftWidth, m.listTableHeight())
	}
	if !wide {
		if !m.isListScreen() {
			lines := []string{m.theme.panel.Render("Selected application")}
			detail, _ := m.detailLines()
			lines = append(lines, detail...)
			return padLines(lines, viewWidth(m.width), m.workspaceHeight()), 0
		}
		lines := []string{m.theme.panel.Render("Applications")}
		if m.screen == screenAvailable {
			lines = append(lines, m.filterView())
		}
		if m.searching {
			lines = append(lines, m.searchInput.View())
		}
		lines = append(lines, strings.Split(m.applicationTable.View(), "\n")...)
		return padLines(lines, viewWidth(m.width), m.workspaceHeight()), 3
	}

	list := make([]string, 0)
	list = append(list, m.theme.panel.Render("Applications"))
	if m.screen == screenAvailable {
		list = append(list, m.filterView())
	}
	if m.searching {
		list = append(list, m.searchInput.View())
	}
	list = append(list, strings.Split(m.applicationTable.View(), "\n")...)
	detail, _ := m.detailLines()
	maxLines := max(len(list), len(detail)+1)
	result := make([]string, maxLines)
	for i := range result {
		left, right := "", ""
		if i < len(list) {
			left = fit(list[i], leftWidth)
		}
		if i == 0 {
			right = m.theme.panel.Render("Selected application")
		} else if i-1 < len(detail) {
			right = detail[i-1]
		}
		result[i] = fit(left, leftWidth) + " │ " + fit(right, max(1, viewWidth(m.width)-leftWidth-3))
	}
	return padLines(result, viewWidth(m.width), m.workspaceHeight()), 3
}

func (m model) listHeaderLines() int {
	if m.screen == screenAvailable {
		return 2 // Applications and filter
	}
	return 1 // Applications
}

func (m model) listTableHeight() int {
	return max(2, m.workspaceHeight()-m.listHeaderLines())
}

func (m *model) configureApplicationTable(values []app.Application, width, height int) {
	usableWidth := max(1, width-3)
	versionWidth := min(16, max(11, width/5))
	nameWidth := max(13, usableWidth-versionWidth-9)
	stateWidth := max(9, usableWidth-nameWidth-versionWidth)
	rows := make([]table.Row, 0, len(values))
	for _, value := range values {
		marker := "  "
		if len(m.selectedIDs) > 0 {
			if m.selectedIDs[value.ID] {
				marker = "[x]"
			} else {
				marker = "[ ]"
			}
		}
		version := value.RegistryVersion
		state := applicationStatus(value)
		if value.InstalledVersion != "" {
			version = value.InstalledVersion
		}
		if m.screen == screenUpdates {
			version, state = value.InstalledVersion, value.RegistryVersion
		}
		rows = append(rows, table.Row{marker + " " + value.Name, version, state})
	}
	if len(rows) == 0 {
		rows = append(rows, table.Row{"No applications.", "", ""})
	}
	versionTitle, stateTitle := "VERSION", "STATUS"
	if m.screen == screenUpdates {
		versionTitle, stateTitle = "INSTALLED", "AVAILABLE"
	}
	columns := []table.Column{{Title: "APPLICATION", Width: nameWidth}, {Title: versionTitle, Width: versionWidth}, {Title: stateTitle, Width: stateWidth}}
	if len(values) == 0 {
		columns = []table.Column{{Title: "APPLICATION", Width: width}, {Width: 0}, {Width: 0}}
	}
	m.applicationTable.SetColumns(columns)
	m.applicationTable.SetRows(rows)
	m.applicationTable.SetWidth(max(1, width))
	m.applicationTable.SetHeight(max(2, height))
	cursor := 0
	if len(rows) > 0 {
		cursor = min(m.selected, len(rows)-1)
	}
	m.applicationTable.SetCursor(max(0, cursor))
}

func (m model) detailLines() ([]string, bool) {
	if len(m.selectedIDs) > 0 {
		lines := []string{m.theme.warning.Render(fmt.Sprintf("%d applications selected", len(m.selectedIDs)))}
		for _, value := range m.visibleApplications() {
			if m.selectedIDs[value.ID] {
				lines = append(lines, "  "+value.Name)
			}
		}
		return lines, true
	}
	if m.detail == nil {
		return []string{"Select an application to inspect it."}, false
	}
	if m.screen == screenVersions {
		lines := []string{m.detail.Name + " / Versions", versionHeading(max(1, viewWidth(m.width)-leftDetailWidth(m.width)-3))}
		for _, value := range m.versions {
			lines = append(lines, versionRow(value, max(1, viewWidth(m.width)-leftDetailWidth(m.width)-3)))
		}
		if m.width > 0 && m.height > 0 {
			m.versionsViewport.SetContent(strings.Join(lines, "\n"))
		}
		if m.focus == focusDetail && m.width > 0 && m.height > 0 {
			return strings.Split(m.versionsViewport.View(), "\n"), true
		}
		return lines, true
	}
	lines := []string{m.theme.accent.Render(m.detail.Name)}
	if m.screen == screenDetails {
		lines = []string{m.breadcrumb() + " / " + m.detail.Name}
	}
	if m.detail.Summary != "" {
		lines = append(lines, m.detail.Summary)
	}
	addDetailFields(&lines, *m.detail, max(1, viewWidth(m.width)-leftDetailWidth(m.width)-3), m.theme)
	if m.width > 0 && m.height > 0 {
		m.detailViewport.SetContent(strings.Join(lines, "\n"))
	}
	if m.focus == focusDetail && m.width > 0 && m.height > 0 {
		return strings.Split(m.detailViewport.View(), "\n"), true
	}
	return lines, true
}

func leftDetailWidth(width int) int { return max(28, (viewWidth(width)*46)/100) }

func (m model) primaryActionLabel() string {
	if m.detail == nil {
		return "Open"
	}
	if m.detail.InstalledVersion == "" {
		return "Install"
	}
	if m.detail.UpdateAvailable && !m.detail.Pinned {
		return "Update"
	}
	return "Inspect"
}

func (m model) overlayContent(width int) []string {
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
	lines := []string{m.theme.panel.Render(fit(title, width)), fit(message, width)}
	if m.screen == screenInstallConfirm {
		for _, conflict := range m.pathConflicts {
			lines = append(lines, fit(conflict.Type+": "+conflict.Directory+" "+conflict.Candidate, width))
		}
	}
	if m.screen == screenInstallChannel {
		for i, channel := range m.channels {
			prefix := "  "
			if i == m.channelSelected {
				prefix = "> "
			}
			lines = append(lines, fit(prefix+channel, width))
		}
	}
	confirm := "[ Cancel ]  [Enter] Confirm"
	if m.screen == screenInstallConfirm {
		confirm = "[ Cancel ]  [Enter] Install anyway"
	}
	return append(lines, "", fit(confirm, width))
}

func (m model) helpOverlayLines() []string {
	bindings := make([]keypkg.Binding, 0, len(m.contextualActionPolicy()))
	for _, action := range m.contextualActionPolicy() {
		bindings = append(bindings, action.binding)
	}
	m.help.SetWidth(max(1, viewWidth(m.width)-4))
	return []string{m.theme.panel.Render("Keyboard reference"), m.help.FullHelpView([][]keypkg.Binding{bindings})}
}

func (m model) headerLine() string {
	left := m.theme.panel.Render("TarLink")
	right := ""
	switch m.versionState {
	case versionChecking:
		right = "Checking for TarLink updates…"
	case versionAvailable:
		right = "TarLink " + m.tarlinkVersion.Latest + " available"
	case versionUnavailable:
		right = "TarLink update check unavailable"
	default:
		if m.tarlinkVersion.Current != "" {
			right = "TarLink " + m.tarlinkVersion.Current
		}
	}
	if viewWidth(m.width) < displaywidth.String(left)+displaywidth.String(right)+3 {
		return fit(left+" "+right, m.width)
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

func (m model) breadcrumb() string {
	var value string
	switch m.screen {
	case screenAvailable:
		value = "Browse"
	case screenInstalled:
		value = "Installed"
	case screenUpdates:
		value = "Updates"
	case screenDetails:
		value = "Browse / " + detailName(m.detail)
	case screenVersions:
		value = detailName(m.detail) + " / Versions"
	case screenRollback:
		value = detailName(m.detail) + " / Rollback"
	case screenUninstall:
		value = detailName(m.detail) + " / Uninstall"
	case screenUpgrade:
		value = "TarLink / Upgrade"
	case screenInstallConfirm:
		value = "Installing / " + detailName(m.detail)
	case screenInstallChannel:
		value = detailName(m.detail) + " / Install"
	}
	return fit(value, m.width)
}

func (m model) separator() string { return strings.Repeat("─", viewWidth(m.width)) }
func detailName(value *app.Application) string {
	if value == nil {
		return ""
	}
	return value.Name
}

func (m model) formattedFooter() string {
	if m.helpOverlay {
		return "Esc Close"
	}
	return m.helpView()
}

func (m model) updateMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	// Layers own the complete input surface while open. Nothing underneath a
	// modal or help overlay is allowed to receive clicks or wheel events.
	if m.helpOverlay || (m.isOverlay() && m.screen != screenInstallChannel) {
		return m, nil
	}
	if m.searching || m.busy != "" {
		return m, nil
	}
	mouse := message.Mouse()
	l := m.layout()
	if mouse.Button == tea.MouseLeft {
		if l.Mode == modeWide && l.Details.contains(mouse.X, mouse.Y) {
			m.focus = focusDetail
			m.applicationTable.Blur()
			return m, nil
		}
		if l.Applications.contains(mouse.X, mouse.Y) {
			m.focus = focusList
			m.applicationTable.Focus()
		}
	}
	if m.focus == focusDetail && (mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown) {
		var cmd tea.Cmd
		m.detailViewport, cmd = m.detailViewport.Update(message)
		return m, cmd
	}
	if m.screen == screenInstallChannel {
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		start, rows := m.channelBounds()
		index := mouse.Y - start
		if index >= 0 && index < rows {
			m.channelSelected = index
		}
		return m, nil
	}
	if (mouse.Button == tea.MouseWheelUp || mouse.Button == tea.MouseWheelDown) && m.isListScreen() {
		delta := 3
		if mouse.Button == tea.MouseWheelUp {
			delta = -delta
		}
		m.moveSelection(delta)
		return m, nil
	}
	if mouse.Button != tea.MouseLeft || !m.isListScreen() {
		return m, nil
	}
	start, rows := m.listBounds()
	if mouse.Y < start || mouse.Y >= start+rows {
		return m, nil
	}
	index := m.listOffset + mouse.Y - start
	if index >= 0 && index < len(m.visibleApplications()) {
		m.selected = index
		m.clampSelection()
		// A row click has the same activation semantics as Enter: select and
		// open the application details in one interaction.
		value := m.visibleApplications()[index]
		m.detail = &value
		m.channels = nil
		m.channelSelected = 0
		m.returnTo = m.screen
		m.screen = screenDetails
	}
	return m, nil
}

func (m model) channelBounds() (start, rows int) {
	layer := m.modalLayer()
	if layer == nil {
		return m.layout().Workspace.y, len(m.channels)
	}
	return layer.GetY() + 3, len(m.channels)
}

func (m model) isListScreen() bool {
	return m.screen == screenAvailable || m.screen == screenInstalled || m.screen == screenUpdates
}

func (m model) listBounds() (start, rows int) {
	start, _ = m.listBoundsWithoutRows()
	start++ // table header occupies the row before the first application
	rows = min(m.listRows(), len(m.visibleApplications())-m.listOffset)
	if rows < 0 {
		rows = 0
	}
	return start, rows
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
		if m.helpOverlay {
			m.previousFocus = m.focus
			m.setFocus(focusOverlay)
		} else {
			m.setFocus(m.previousFocus)
		}
		return m, nil
	}
	if m.helpOverlay {
		if keypkg.Matches(message, bindings.Cancel) {
			m.helpOverlay = false
			m.setFocus(m.previousFocus)
		}
		return m, nil
	}
	if keypkg.Matches(message, bindings.Tab) && !m.isOverlay() {
		if pressed == "shift+tab" {
			if m.focus == focusList {
				m.setFocus(focusDetail)
			} else {
				m.setFocus(focusList)
			}
		} else if m.focus == focusList {
			m.setFocus(focusDetail)
		} else {
			m.setFocus(focusList)
		}
		return m, nil
	}
	if m.focus == focusDetail && !m.searching && !m.isOverlay() && (keypkg.Matches(message, bindings.Up) || keypkg.Matches(message, bindings.Down) || pressed == "pgup" || pressed == "pgdown") {
		var cmd tea.Cmd
		m.detailViewport, cmd = m.detailViewport.Update(message)
		return m, cmd
	}
	if m.searching {
		switch {
		case keypkg.Matches(message, bindings.Cancel):
			if !m.matchesAction(message, actionCancel) {
				return m, nil
			}
			m.searching = false
			m.setFocus(focusList)
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
	if m.isListScreen() && m.matchesAction(message, actionFilter) && pressed == " " {
		visible := m.visibleApplications()
		if len(visible) > 0 && m.selected < len(visible) {
			if m.selectedIDs == nil {
				m.selectedIDs = map[string]bool{}
			}
			id := visible[m.selected].ID
			if m.selectedIDs[id] {
				delete(m.selectedIDs, id)
			} else {
				m.selectedIDs[id] = true
			}
		}
		return m, nil
	}
	if m.screen == screenAvailable && len(m.selectedIDs) > 0 && m.matchesAction(message, actionInstalled) {
		return m.startBatchInstall()
	}
	if m.screen == screenInstalled && len(m.selectedIDs) > 0 && m.matchesAction(message, actionUpdates) {
		m.batchIDs = m.selectedIDsInOrder(m.installed)
		m.batchUninstall = true
		m.confirmTo, m.confirmSet, m.screen = screenInstalled, true, screenUninstall
		return m, nil
	}
	switch {
	case m.matchesAction(message, actionUpgrade):
		if m.upgradeAvailable {
			m.clearFeedback()
			m.returnTo = m.screen
			m.confirmSet = false
			m.openOverlay(screenUpgrade)
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
		m.setFocus(focusSearch)
		m.selected = 0
		m.listOffset = 0
	case m.matchesAction(message, actionInstalled):
		m.clearFeedback()
		m.selectedIDs = nil
		m.screen = screenInstalled
		m.selected = 0
		m.listOffset = 0
	case m.matchesAction(message, actionUpdates):
		m.clearFeedback()
		m.selectedIDs = nil
		m.screen = screenUpdates
		m.selected = 0
		m.listOffset = 0
	case m.matchesAction(message, actionCancel):
		m.clearFeedback()
		if m.screen == screenInstallChannel {
			m.closeOverlay(screenDetails)
		} else if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenUninstallConflictConfirm {
			m.closeOverlay(m.confirmationTarget())
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
			if m.detail != nil && m.detail.InstalledVersion == "" && len(m.detail.ChannelHeads) > 1 {
				m.channels = channelNames(m.detail)
				m.channelSelected = channelIndex(m.channels, m.detail.DefaultChannel)
				m.openOverlay(screenInstallChannel)
				return m, nil
			}
			m.channels = nil
			m.channelSelected = 0
			return m.activateSelected()
		}
		visible := m.visibleApplications()
		if len(visible) != 0 {
			m.clearFeedback()
			hadDetail := m.detail != nil
			selected := visible[m.selected]
			m.detail = &selected
			m.returnTo = m.screen
			m.screen = screenDetails
			if !hadDetail {
				return m, nil
			}
			return m.activateSelected()
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
			m.openOverlay(screenRollback)
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
			m.openOverlay(screenUninstall)
		}
	case m.matchesAction(message, actionRemoveConflict):
		if m.screen == screenUninstall && m.uninstallConflict != nil {
			m.confirmTo = screenUninstall
			m.confirmSet = true
			m.openOverlay(screenUninstallConflictConfirm)
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
	m.listOffset = m.selected
	values := m.visibleApplications()
	if m.selected >= 0 && m.selected < len(values) {
		value := values[m.selected]
		m.detail = &value
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
	m.listOffset = 0
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
	m.applicationTable.SetCursor(m.selected)
}

func (m model) listRows() int {
	return max(1, m.listTableHeight()-1)
}

func (m model) listBoundsWithoutRows() (start, rows int) {
	start = chromeHeight + m.listHeaderLines()
	if m.searching {
		start++
	}
	// The applications panel title occupies the first content row inside its
	// border, before the table header.
	start++
	return start, 0
}

func (m model) footer() string {
	return m.helpView()
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

func footerLines(value string, width int) []string {
	width = viewWidth(width)
	return []string{truncate(value, width, "…")}
}

func (m model) progressLine() string {
	stage := title(string(m.progress.Stage))
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
