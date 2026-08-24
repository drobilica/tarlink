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
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
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
	progressBar         progress.Model
	tarlinkVersion      app.TarLinkVersion
	upgradeAvailable    bool
	cancel              context.CancelFunc
	opCancel            context.CancelFunc
	uninstallConflict   *app.UninstallConflict
	focus               paneFocus
	helpOverlay         bool
	componentsReady     bool
	searchInput         textinput.Model
	detailViewport      viewport.Model
	versionsViewport    viewport.Model
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
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampViewport()
		m.help.SetWidth(m.width)
		m.progressBar.SetWidth(progressBarWidthFor(m.width))
		m.searchInput.SetWidth(max(12, m.width-18))
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
		m.screen = screenInstallConfirm
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
	body := m.bodyLines()
	footer := m.style(truncate(m.formattedFooter(), viewWidth(m.width), "…"), muted)
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
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// bodyLines is the shared shell and screen content. Keeping chrome here makes
// the footer and mouse coordinates agree with every screen's rendered layout.
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
		if m.progress.Stage != "" {
			add(m.progressLine())
		}
		lines = append(lines, "")
	}
	if m.upgradeAvailable {
		add(m.style("TarLink update available: "+m.tarlinkVersion.Current+" → "+m.tarlinkVersion.Latest, warning))
	}
	workspace, _ := m.workspaceLines()
	for _, line := range workspace {
		lines = append(lines, fit(line, m.width))
	}
	if m.isOverlay() {
		lines = append(lines, "")
		for _, line := range m.overlayLines() {
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
	m.detailViewport = viewport.New()
	m.detailViewport.SoftWrap = true
	m.versionsViewport = viewport.New()
	m.versionsViewport.SoftWrap = true
	m.focus = focusList
	m.componentsReady = true
}

func (m *model) setViewportSize() {
	contentWidth := max(1, m.width-4)
	contentHeight := max(1, m.height-7)
	m.detailViewport.SetWidth(contentWidth)
	m.detailViewport.SetHeight(contentHeight)
	m.versionsViewport.SetWidth(contentWidth)
	m.versionsViewport.SetHeight(contentHeight)
}

func (m model) isOverlay() bool {
	return m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm || m.screen == screenInstallChannel || m.screen == screenUninstallConflictConfirm
}

func (m model) workspaceLines() ([]string, int) {
	values := m.visibleApplications()
	if m.detail == nil && len(values) > 0 && m.selected < len(values) {
		value := values[m.selected]
		m.detail = &value
	}
	leftWidth := max(28, (viewWidth(m.width)*46)/100)
	wide := viewWidth(m.width) >= 72
	if !wide {
		lines := []string{m.theme.panel.Render("Applications")}
		if m.screen == screenAvailable {
			lines = append(lines, m.filterView())
		}
		if m.searching {
			lines = append(lines, m.searchInput.View())
		} else if m.query != "" {
			lines = append(lines, "Search: "+m.query)
		}
		rows := max(1, m.listRows()-2)
		writeApplicationLinesWithSelection(&lines, values, m.selected, m.listOffset, rows, m.width, m.theme, m.screen == screenUpdates, m.selectedIDs)
		lines = append(lines, m.theme.panel.Render("Selected application"))
		detail, _ := m.detailLines()
		lines = append(lines, detail...)
		return lines, 3
	}

	list := make([]string, 0)
	list = append(list, m.theme.panel.Render("Applications"))
	if m.screen == screenAvailable {
		list = append(list, m.filterView())
	}
	if m.searching {
		list = append(list, m.searchInput.View())
	} else if m.query != "" {
		list = append(list, "Search: "+m.query)
	}
	writeApplicationLinesWithSelection(&list, values, m.selected, m.listOffset, max(1, m.listRows()-2), leftWidth, m.theme, m.screen == screenUpdates, m.selectedIDs)
	detail, _ := m.detailLines()
	if len(detail) == 0 && m.detail != nil {
		detail = []string{m.breadcrumb(), m.detail.Name}
		if m.detail.Summary != "" {
			detail = append(detail, m.detail.Summary)
		}
		addDetailFields(&detail, *m.detail, max(1, viewWidth(m.width)-leftDetailWidth(m.width)), m.theme)
	}
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
	return result, 3
}

func (m model) detailLines() ([]string, bool) {
	if len(m.selectedIDs) > 0 {
		lines := []string{m.theme.warning.Render(fmt.Sprintf("%d applications selected", len(m.selectedIDs)))}
		for _, value := range m.visibleApplications() {
			if m.selectedIDs[value.ID] {
				lines = append(lines, "  "+value.Name)
			}
		}
		if len(m.selectedIDs) > 0 {
			lines = append(lines, "", m.buttonLine())
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
		return lines, true
	}
	lines := []string{m.theme.accent.Render(m.detail.Name)}
	if m.screen == screenDetails {
		lines = append([]string{m.breadcrumb()}, lines...)
	}
	if m.detail.Summary != "" {
		lines = append(lines, m.detail.Summary)
	}
	lines = append(lines, "")
	addDetailFields(&lines, *m.detail, max(1, viewWidth(m.width)-leftDetailWidth(m.width)-3), m.theme)
	lines = append(lines, "", m.buttonLine())
	if m.width > 0 && m.height > 0 {
		m.detailViewport.SetContent(strings.Join(lines, "\n"))
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

func (m model) buttonLine() string {
	parts := make([]string, 0, 4)
	for _, a := range m.contextualActionPolicy() {
		if a.id == actionEnter || a.id == actionVersions || a.id == actionRollback || a.id == actionUninstall || a.id == actionInstalled || a.id == actionUpdates {
			label := a.label
			if i := strings.IndexByte(label, ' '); i >= 0 {
				label = label[i+1:]
			}
			style := m.theme.controlSelected
			if a.id == actionUninstall {
				style = m.theme.danger
			}
			parts = append(parts, style.Render("[ "+label+" ]"))
		}
	}
	return strings.Join(parts, "  ")
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
	lines := []string{m.theme.panel.Render("┌─ " + title + " ─────────────────────────┐"), "│ " + message}
	if m.screen == screenInstallConfirm {
		for _, conflict := range m.pathConflicts {
			lines = append(lines, "│ "+conflict.Type+": "+conflict.Directory+" "+conflict.Candidate)
		}
	}
	if m.screen == screenInstallChannel {
		for i, channel := range m.channels {
			prefix := "  "
			if i == m.channelSelected {
				prefix = "> "
			}
			lines = append(lines, "│ "+prefix+channel)
		}
	}
	confirm := "Confirm"
	if m.screen == screenInstallConfirm {
		confirm = "Install anyway"
	}
	lines = append(lines, "│", "│        [ Cancel ]  [Enter] "+confirm, "└──────────────────────────────────┘")
	return lines
}

func (m model) helpOverlayLines() []string {
	bindings := []keypkg.Binding{newKeyMap().Up, newKeyMap().Down, newKeyMap().Enter, newKeyMap().Search, newKeyMap().Versions, newKeyMap().Rollback, newKeyMap().Uninstall, newKeyMap().Cancel, newKeyMap().Quit}
	m.help.SetWidth(max(1, viewWidth(m.width)-4))
	return []string{m.theme.panel.Render("┌─ Keyboard reference ──────────────┐"), m.help.FullHelpView([][]keypkg.Binding{bindings}), "└────────────────────────────────────┘"}
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
	labels := make([]string, 0, len(m.contextualActionPolicy()))
	for _, action := range m.contextualActionPolicy() {
		label := action.label
		if label == "Navigate" {
			label = "[↑↓] Navigate"
		} else if label == "Open" {
			label = "[Enter] Open"
		} else {
			parts := strings.SplitN(label, " ", 2)
			label = "[" + parts[0] + "]"
			if len(parts) == 2 {
				label += " " + parts[1]
			}
		}
		if len(labels) == 0 || labels[len(labels)-1] != label {
			labels = append(labels, label)
		}
	}
	value := strings.Join(labels, "  ")
	if m.isOverlay() {
		value = strings.ReplaceAll(value, "[Enter]", "[↵]")
	}
	return value
}

func (m model) updateMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.searching || m.busy != "" {
		return m, nil
	}
	mouse := message.Mouse()
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
		m.clampViewport()
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
	workspace, _ := m.workspaceLines()
	return 3 + len(workspace) + 3, len(m.channels)
}

func (m model) isListScreen() bool {
	return m.screen == screenAvailable || m.screen == screenInstalled || m.screen == screenUpdates
}

func (m model) listBounds() (start, rows int) {
	start, _ = m.listBoundsWithoutRows()
	rows = min(m.listRows(), len(m.visibleApplications())-m.listOffset)
	if rows < 0 {
		rows = 0
	}
	if m.height > 0 {
		footerRows := len(footerLines(m.footer(), m.width))
		rows = min(rows, max(0, m.height-footerRows-start-1))
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
			m.focus = focusOverlay
		} else {
			m.focus = focusList
		}
		return m, nil
	}
	if m.helpOverlay {
		if keypkg.Matches(message, bindings.Cancel) {
			m.helpOverlay = false
			m.focus = focusList
		}
		return m, nil
	}
	if keypkg.Matches(message, bindings.Tab) && !m.isOverlay() {
		if pressed == "shift+tab" {
			if m.focus == focusList {
				m.focus = focusDetail
			} else {
				m.focus = focusList
			}
		} else if m.focus == focusList {
			m.focus = focusDetail
		} else {
			m.focus = focusList
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
			m.searchInput.Blur()
			m.focus = focusList
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
	if m.isListScreen() && pressed == " " {
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
	if m.screen == screenAvailable && len(m.selectedIDs) > 0 && pressed == "i" {
		return m.startBatchInstall()
	}
	if m.screen == screenInstalled && len(m.selectedIDs) > 0 && pressed == "u" {
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
		m.focus = focusSearch
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
	m.selected += delta
	if m.selected < 0 {
		m.selected = 0
	}
	if m.selected >= length {
		m.selected = length - 1
	}
	m.clampViewport()
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
	m.clampViewport()
}

func (m *model) clampViewport() {
	rows := m.listRows()
	if rows < 1 {
		rows = 1
	}
	if m.selected < m.listOffset {
		m.listOffset = m.selected
	}
	if m.selected >= m.listOffset+rows {
		m.listOffset = m.selected - rows + 1
	}
	if m.listOffset < 0 {
		m.listOffset = 0
	}
}

func (m model) listRows() int {
	start, _ := m.listBoundsWithoutRows()
	footerRows := len(footerLines(m.footer(), m.width))
	rows := m.height - start - 1 - footerRows // trailing spacer before the footer
	if rows < 1 {
		return 1
	}
	return rows
}

func (m model) listBoundsWithoutRows() (start, rows int) {
	start = 3 // header, breadcrumb, separator
	if m.status != "" {
		start += 2
	}
	if m.err != nil {
		start += 3
	}
	if m.busy != "" {
		start++
		if m.progress.Stage != "" {
			start++
		}
		start++
	}
	if m.upgradeAvailable {
		start++
	}
	if m.isListScreen() {
		start++ /* Applications heading */
		if m.screen == screenAvailable {
			start++ /* collection filter */
		}
		start++ /* column heading */
	}
	if m.isListScreen() {
		start--
	}
	if m.searching || m.query != "" {
		start++
	}
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

func writeApplications(destination *strings.Builder, values []app.Application, selected, offset, rows, width int, theme tuiTheme) {
	lines := make([]string, 0)
	writeApplicationLines(&lines, values, selected, offset, rows, width, theme, false)
	for _, line := range lines {
		destination.WriteString(line + "\n")
	}
}

func writeApplicationLines(destination *[]string, values []app.Application, selected, offset, rows, width int, theme tuiTheme, updatesTable bool) {
	writeApplicationLinesWithSelection(destination, values, selected, offset, rows, width, theme, updatesTable, nil)
}

func writeApplicationLinesWithSelection(destination *[]string, values []app.Application, selected, offset, rows, width int, theme tuiTheme, updatesTable bool, selectedIDs map[string]bool) {
	if updatesTable {
		*destination = append(*destination, applicationColumns(width, true))
	} else {
		*destination = append(*destination, applicationColumns(width, false))
	}
	if len(values) == 0 {
		*destination = append(*destination, truncate("No applications.", viewWidth(width), "…"))
		return
	}
	end := min(len(values), offset+rows)
	for index := offset; index < end; index++ {
		value := values[index]
		marker := "  "
		if selectedIDs != nil {
			if selectedIDs[value.ID] {
				marker = "[x] "
			} else {
				marker = "[ ] "
			}
		}
		if index == selected {
			marker = "> "
		}
		if width <= 2 {
			marker = ""
		}
		terminalWidth := viewWidth(width)
		row := marker + applicationRow(value, terminalWidth, updatesTable)
		if index == selected {
			row = theme.selected.Render(row)
		}
		*destination = append(*destination, fit(row, width))
	}
}

func applicationColumns(width int, updateTable bool) string {
	if viewWidth(width) < 60 {
		if updateTable {
			return "  APPLICATION  INSTALLED  AVAILABLE"
		}
		return "  APPLICATION  VERSION  STATUS"
	}
	if updateTable {
		return "  " + columnLayout("APPLICATION", "INSTALLED", "AVAILABLE", viewWidth(width))
	}
	return "  " + columnLayout("APPLICATION", "VERSION", "STATUS", viewWidth(width))
}

func applicationRow(value app.Application, width int, updateTable bool) string {
	if width < 60 {
		label := value.Name
		if value.UpdateAvailable {
			label += "  ↑ Update available"
		} else if value.InstalledVersion != "" {
			label += "  ✓ Installed"
		}
		return truncate(label, max(1, width-2), "…")
	}
	name := value.Name
	if updateTable {
		return columnLayout(name, value.InstalledVersion, value.RegistryVersion, width-2)
	}
	version := value.RegistryVersion
	if value.InstalledVersion != "" {
		version = value.InstalledVersion
	}
	return columnLayout(name, version, installedLabel(value), width-2)
}

func columnLayout(first, second, third string, width int) string {
	width = max(18, width)
	firstWidth := min(34, max(12, (width-4)/2))
	secondWidth := min(16, max(8, width/5))
	thirdWidth := max(1, width-firstWidth-secondWidth-4)
	firstValue := truncate(first, firstWidth, "…")
	secondValue := truncate(second, secondWidth, "…")
	return firstValue + strings.Repeat(" ", firstWidth-displaywidth.String(firstValue)+2) + secondValue + strings.Repeat(" ", secondWidth-displaywidth.String(secondValue)+2) + truncate(third, thirdWidth, "…")
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
