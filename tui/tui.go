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
	"unicode/utf8"

	"charm.land/bubbles/v2/help"
	keypkg "charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
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

type operationMsg struct {
	message      string
	err          error
	next         screen
	move         bool
	clearUpgrade bool
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
}

// Run starts the terminal renderer. All application changes are delegated to
// the same service API used by the CLI.
func Run(ctx context.Context, service app.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return fmt.Errorf("TarLink core is unavailable")
	}
	operationContext, cancel := context.WithCancel(ctx)
	defer cancel()
	program := tea.NewProgram(
		model{ctx: operationContext, service: service, screen: screenAvailable, color: colorEnabled(output), theme: newTheme(colorEnabled(output)), help: newHelp(colorEnabled(output)), progressBar: newProgress(colorEnabled(output)), cancel: cancel},
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output),
	)
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return tea.Batch(m.loadCmd(), m.checkVersionCmd()) }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.clampViewport()
		m.help.SetWidth(m.width)
		m.progressBar.SetWidth(progressBarWidthFor(m.width))
		return m, nil
	case loadedMsg:
		m.busy = ""
		m.err = message.err
		if message.err == nil {
			m.available = message.available
			m.installed = message.installed
			m.refreshDetail()
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
		return m.startInstall(message.appID)
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
		m.status = message.message
		if errors.Is(message.err, context.Canceled) {
			m.err = nil
			m.status = "Operation cancelled"
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
	var body strings.Builder
	line := func(value string) {
		for _, part := range strings.Split(value, "\n") {
			body.WriteString(fit(part, m.width))
			body.WriteByte('\n')
		}
	}
	line(m.style("TarLink", accent))
	if m.upgradeAvailable {
		line(m.style("UPDATE AVAILABLE "+m.tarlinkVersion.Current+" -> "+m.tarlinkVersion.Latest+" [U] (press U to upgrade)", warning))
	}
	if m.busy != "" {
		line(m.style(m.busy+"...", accent))
		if m.progress.Stage != "" {
			line(m.progressLine())
		}
		body.WriteByte('\n')
	}
	if m.err != nil {
		line(m.style("OPERATION FAILED", danger))
		line(m.style(m.err.Error(), danger))
		body.WriteByte('\n')
	}
	if m.status != "" {
		line(m.style(m.status, success))
		body.WriteByte('\n')
	}

	switch m.screen {
	case screenAvailable:
		line(m.style("APPLICATIONS", accent))
		line(m.filterView())
		if m.searching {
			line("Search /" + m.query + "_")
			body.WriteByte('\n')
		} else if m.query != "" {
			line("Search /" + m.query)
			body.WriteByte('\n')
		} else {
			body.WriteString("\n")
		}
		writeApplications(&body, m.visibleApplications(), m.selected, m.listOffset, m.listRows(), m.width, m.theme)
	case screenInstalled:
		line(m.style("INSTALLED", accent))
		body.WriteByte('\n')
		writeApplications(&body, m.installed, m.selected, m.listOffset, m.listRows(), m.width, m.theme)
	case screenUpdates:
		line(m.style("UPDATES", accent))
		body.WriteByte('\n')
		writeApplications(&body, updates(m.installed), m.selected, m.listOffset, m.listRows(), m.width, m.theme)
	case screenDetails:
		line(m.style("APPLICATION DETAILS", accent))
		body.WriteByte('\n')
		if m.detail != nil {
			line(m.style(m.detail.Name, accent))
			body.WriteByte('\n')
			line(m.detail.Summary)
			body.WriteByte('\n')
			line("ID: " + m.detail.ID)
			line("Status: " + applicationStatus(*m.detail))
			if m.detail.InstalledVersion != "" {
				line("Installed version: " + m.detail.InstalledVersion)
			}
			if m.detail.InstalledChannel != "" {
				line("Channel: " + m.detail.InstalledChannel)
			}
			if m.detail.Pinned {
				line("Pinned: yes")
			}
			line("Available version: " + m.detail.RegistryVersion)
			line("Categories: " + strings.Join(m.detail.Categories, ", "))
			if hasGameData(*m.detail) {
				line("Requires: Original game data")
			}
			line("Homepage: " + m.detail.Homepage)
			if m.detail.InstalledVersion == "" {
				body.WriteByte('\n')
				line("Enter Install   Esc Back   q Quit")
			} else if m.detail.Pinned {
				body.WriteByte('\n')
				line("Pinned — v Versions   r Rollback   x Uninstall   Esc Back   q Quit")
			} else if m.detail.UpdateAvailable {
				body.WriteByte('\n')
				line("Enter Update   v Versions   r Rollback   x Uninstall")
			} else {
				body.WriteByte('\n')
				line("v Versions   r Rollback   x Uninstall   Esc Back   q Quit")
			}
		}
	case screenVersions:
		line(m.style("VERSIONS", accent))
		body.WriteByte('\n')
		for _, value := range m.versions {
			line(truncate(value.Version, max(1, m.width/2), " ") + " " + truncate(value.Status, max(1, m.width/2-1), "…"))
		}
	case screenRollback:
		line(m.style("ROLLBACK", warning))
		body.WriteByte('\n')
		if m.detail != nil {
			line("Switch " + m.detail.Name + " from " + m.detail.InstalledVersion + " to its retained previous version?")
			body.WriteByte('\n')
			line("Enter Confirm   Esc Cancel")
		}
	case screenUninstall:
		line(m.style("UNINSTALL", warning))
		body.WriteByte('\n')
		if m.detail != nil {
			line("Remove " + m.detail.Name + " (" + m.detail.ID + ") and its installed files?")
			body.WriteByte('\n')
			line("This action cannot be undone.")
			body.WriteByte('\n')
			line("Enter Confirm   Esc Cancel")
		} else {
			line("No installed application selected.")
			body.WriteByte('\n')
			line("Esc Cancel")
		}
	case screenUpgrade:
		line(m.style("TARLINK UPGRADE", warning))
		body.WriteByte('\n')
		line(m.tarlinkVersion.Current + " → " + m.tarlinkVersion.Latest)
		body.WriteByte('\n')
		line("Enter Upgrade   Esc Cancel")
	case screenInstallConfirm:
		line(m.style("INSTALL PATH CONFLICT", warning))
		body.WriteByte('\n')
		if m.detail != nil {
			line("Installing " + m.detail.Name + " (" + m.detail.ID + ") may be shadowed or hidden by your PATH.")
			body.WriteByte('\n')
			for _, conflict := range m.pathConflicts {
				switch conflict.Type {
				case "not_in_path":
					line("- " + conflict.Directory + " is not in your PATH; " + m.detail.ID + " would not run as a command.")
				case "shadowed":
					line("- " + conflict.Directory + " shadows " + m.detail.ID + "; running \"" + m.detail.ID + "\" would use " + conflict.Candidate + ".")
				}
			}
			body.WriteByte('\n')
			line("Enter Install anyway   Esc Cancel")
		}
	}

	body.WriteByte('\n')
	footerContent := footerLines(m.helpView(), m.width)
	if m.height > 0 && len(footerContent) > m.height {
		footerContent = footerContent[len(footerContent)-m.height:]
	}
	for _, footerLine := range footerContent {
		line(m.style(footerLine, muted))
	}
	content := strings.TrimSuffix(body.String(), "\n")
	lines := strings.Split(content, "\n")
	if m.height > 0 && len(lines) > m.height {
		keep := m.height - len(footerContent)
		if keep < 0 {
			keep = 0
		}
		lines = append(lines[:keep], lines[len(lines)-len(footerContent):]...)
	}
	view := tea.NewView(strings.Join(lines, "\n") + "\n")
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

func (m model) updateMouse(message tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.searching || m.busy != "" {
		return m, nil
	}
	mouse := message.Mouse()
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
	}
	return m, nil
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
	pressed := message.String()
	bindings := m.keyMap()
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
		switch pressed {
		case "esc":
			m.searching = false
			return m, nil
		case "enter":
			m.searching = false
			m.busy = "Searching"
			cmd, cancel := m.searchCmd()
			m.opCancel = cancel
			return m, cmd
		case "backspace", "ctrl+h":
			if m.query != "" {
				_, size := utf8.DecodeLastRuneInString(m.query)
				m.query = m.query[:len(m.query)-size]
			}
			return m, nil
		default:
			if utf8.RuneCountInString(pressed) == 1 {
				r, _ := utf8.DecodeRuneInString(pressed)
				if r >= ' ' && r != utf8.RuneError && len(m.query) < 128 {
					m.query += pressed
				}
			}
			return m, nil
		}
	}

	if keypkg.Matches(message, bindings.Quit) {
		if m.opCancel != nil {
			m.opCancel()
		}
		if m.cancel != nil {
			m.cancel()
		}
		return m, tea.Quit
	}
	if m.busy != "" {
		if keypkg.Matches(message, bindings.Cancel) && m.opCancel != nil {
			m.opCancel()
		}
		return m, nil
	}
	switch {
	case keypkg.Matches(message, bindings.Upgrade):
		if m.upgradeAvailable {
			m.clearFeedback()
			m.returnTo = m.screen
			m.confirmSet = false
			m.screen = screenUpgrade
		}
	case keypkg.Matches(message, bindings.Up):
		m.clearFeedback()
		m.moveSelection(-1)
	case keypkg.Matches(message, bindings.Down):
		m.clearFeedback()
		m.moveSelection(1)
	case keypkg.Matches(message, bindings.Left):
		if m.screen == screenAvailable {
			m.clearFeedback()
			m.changeFilter(-1)
		}
	case keypkg.Matches(message, bindings.Right):
		if m.screen == screenAvailable {
			m.clearFeedback()
			m.changeFilter(1)
		}
	case keypkg.Matches(message, bindings.Search):
		m.clearFeedback()
		m.screen = screenAvailable
		m.searching = true
		m.query = ""
		m.selected = 0
		m.listOffset = 0
	case keypkg.Matches(message, bindings.Installed):
		m.clearFeedback()
		m.screen = screenInstalled
		m.selected = 0
		m.listOffset = 0
	case keypkg.Matches(message, bindings.Updates):
		m.clearFeedback()
		m.screen = screenUpdates
		m.selected = 0
		m.listOffset = 0
	case keypkg.Matches(message, bindings.Cancel):
		m.clearFeedback()
		if m.screen == screenRollback || m.screen == screenUninstall || m.screen == screenUpgrade || m.screen == screenInstallConfirm {
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
		}
	case keypkg.Matches(message, bindings.Enter):
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
			if id := m.selectedID(); id != "" && m.selectedInstalled() {
				m.busy = "Uninstalling"
				m.startOperation()
				cmd, cancel := m.uninstallCmd(id)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenInstallConfirm {
			if id := m.selectedID(); id != "" {
				m.busy = "Installing " + id
				m.startOperation()
				cmd, cancel := m.installCmd(id)
				m.opCancel = cancel
				return m, cmd
			}
			return m, nil
		}
		if m.screen == screenDetails {
			return m.activateSelected()
		}
		visible := m.visibleApplications()
		if len(visible) != 0 {
			m.clearFeedback()
			selected := visible[m.selected]
			m.detail = &selected
			m.returnTo = m.screen
			m.screen = screenDetails
		}
	case keypkg.Matches(message, bindings.Versions):
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
	case keypkg.Matches(message, bindings.Rollback):
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
	case keypkg.Matches(message, bindings.Uninstall):
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
		return operationMsg{message: "Uninstalled " + id, next: next, move: true}, err
	})
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
			parts = append(parts, "["+label+"]")
		} else {
			parts = append(parts, label)
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
	start = 1 // title row
	if m.upgradeAvailable {
		start++ // update banner row
	}
	if m.busy != "" {
		start++
		if m.progress.Stage != "" {
			start++
		}
		start++ // blank after the busy block
	}
	if m.err != nil {
		start += 3 // failure heading + detail + blank
	}
	if m.status != "" {
		start += 2 // status + blank
	}
	start++ // screen header
	if m.screen == screenAvailable {
		start++ // filter row
		if m.searching || m.query != "" {
			start++ // search row
		}
		start++ // blank separator before the list
	} else {
		start++ // blank separator before the list
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
	if len(values) == 0 {
		destination.WriteString(truncate("No applications.", viewWidth(width), "…") + "\n")
		return
	}
	end := min(len(values), offset+rows)
	for index := offset; index < end; index++ {
		value := values[index]
		marker := "  "
		if index == selected {
			marker = "> "
		}
		if width <= 2 {
			marker = ""
		}
		width = viewWidth(width)
		name := truncate(value.Name, max(1, width-4), "…")
		status := installedLabel(value)
		var row string
		if width >= 60 {
			name = truncate(value.Name, min(24, max(1, width/3)), "…")
			status = truncate(status, min(24, max(1, width/3)), "…")
			row = marker + name + strings.Repeat(" ", max(1, width-4-displaywidth.String(name)-displaywidth.String(status))) + status
		} else if width >= 38 {
			row = marker + name + " " + truncate(status, max(1, width-3-displaywidth.String(name)), "…")
		} else {
			row = marker + truncate(value.Name, max(1, width-2), "…")
		}
		if index == selected {
			row = theme.selected.Render(row)
		}
		destination.WriteString(row + "\n")
	}
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
	if displaywidth.String(value) <= width {
		return []string{value}
	}
	compact := "↑/↓ Select  Enter  Esc Back  q Quit"
	if displaywidth.String(compact) <= width {
		return []string{compact}
	}
	return []string{"↑/↓ Select  Enter  Esc Back", "q Quit"}
}

func (m model) progressLine() string {
	stage := title(string(m.progress.Stage))
	if m.progress.Stage == app.ProgressComplete {
		return m.style(stage, success)
	}
	line := stage
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
