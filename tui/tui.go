// Package tui is TarLink's presentation-only terminal interface.
package tui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
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
)

type loadedMsg struct {
	available []app.Application
	installed []app.Application
	err       error
}

type searchMsg struct {
	values []app.Application
	err    error
}

type versionsMsg struct {
	values []app.Version
	err    error
}

type operationMsg struct {
	message string
	err     error
	next    screen
	move    bool
}

type model struct {
	ctx       context.Context
	service   app.Service
	screen    screen
	returnTo  screen
	available []app.Application
	installed []app.Application
	versions  []app.Version
	selected  int
	detail    *app.Application
	searching bool
	query     string
	busy      string
	status    string
	err       error
}

// Run starts the terminal renderer. All application changes are delegated to
// the same service API used by the CLI.
func Run(ctx context.Context, service app.Service, input io.Reader, output io.Writer) error {
	if service == nil {
		return fmt.Errorf("TarLink core is unavailable")
	}
	program := tea.NewProgram(
		model{ctx: ctx, service: service, screen: screenAvailable},
		tea.WithContext(ctx), tea.WithInput(input), tea.WithOutput(output),
	)
	_, err := program.Run()
	return err
}

func (m model) Init() tea.Cmd { return m.loadCmd() }

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
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
	case searchMsg:
		m.busy = ""
		m.err = message.err
		if message.err == nil {
			m.available = message.values
		}
		m.selected = 0
		return m, nil
	case versionsMsg:
		m.busy = ""
		m.err = message.err
		if message.err == nil {
			m.versions = message.values
			m.screen = screenVersions
		}
		return m, nil
	case operationMsg:
		m.busy = ""
		m.err = message.err
		m.status = message.message
		if message.err != nil {
			return m, nil
		}
		if message.move {
			m.screen = message.next
		}
		return m, m.loadCmd()
	case tea.KeyPressMsg:
		return m.updateKey(message)
	}
	return m, nil
}

func (m model) View() tea.View {
	var body strings.Builder
	body.WriteString("TarLink\n\n")
	if m.busy != "" {
		fmt.Fprintf(&body, "%s...\n\n", m.busy)
	}
	if m.err != nil {
		fmt.Fprintf(&body, "Error: %s\n\n", m.err)
	}
	if m.status != "" {
		fmt.Fprintf(&body, "%s\n\n", m.status)
	}

	switch m.screen {
	case screenAvailable:
		body.WriteString("AVAILABLE / SEARCH\n")
		if m.searching {
			fmt.Fprintf(&body, "Search /%s_\n\n", m.query)
		} else if m.query != "" {
			fmt.Fprintf(&body, "Search /%s\n\n", m.query)
		} else {
			body.WriteString("\n")
		}
		writeApplications(&body, m.available, m.selected)
	case screenInstalled:
		body.WriteString("INSTALLED\n\n")
		writeApplications(&body, m.installed, m.selected)
	case screenUpdates:
		body.WriteString("UPDATES\n\n")
		writeApplications(&body, updates(m.installed), m.selected)
	case screenDetails:
		body.WriteString("APPLICATION DETAILS\n\n")
		if m.detail != nil {
			fmt.Fprintf(&body, "%s\n\n%s\n\nID: %s\nRegistry: %s\nInstalled: %s\nCategories: %s\nHomepage: %s\n",
				m.detail.Name, m.detail.Summary, m.detail.ID, m.detail.RegistryVersion,
				installedLabel(*m.detail), strings.Join(m.detail.Categories, ", "), m.detail.Homepage)
		}
	case screenVersions:
		body.WriteString("VERSIONS\n\n")
		for _, value := range m.versions {
			fmt.Fprintf(&body, "%-20s %s\n", value.Version, value.Status)
		}
	case screenRollback:
		body.WriteString("ROLLBACK\n\n")
		if m.detail != nil {
			fmt.Fprintf(&body, "Switch %s from %s to its retained previous version?\n\nEnter Confirm   Esc Cancel\n", m.detail.Name, m.detail.InstalledVersion)
		}
	}

	body.WriteString("\n↑/↓ Select  Enter Open/Act  Esc Back  / Search  i Installed  u Updates  v Versions  r Rollback  q Quit\n")
	return tea.NewView(body.String())
}

func (m model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			return m, nil
		case "enter":
			m.searching = false
			m.busy = "Searching"
			return m, m.searchCmd()
		case "backspace", "ctrl+h":
			if m.query != "" {
				_, size := utf8.DecodeLastRuneInString(m.query)
				m.query = m.query[:len(m.query)-size]
			}
			return m, nil
		default:
			if utf8.RuneCountInString(key) == 1 {
				r, _ := utf8.DecodeRuneInString(key)
				if r >= ' ' && r != utf8.RuneError && len(m.query) < 128 {
					m.query += key
				}
			}
			return m, nil
		}
	}

	if key == "q" || key == "ctrl+c" {
		return m, tea.Quit
	}
	if m.busy != "" {
		return m, nil
	}
	switch key {
	case "up":
		if m.selected > 0 {
			m.selected--
		}
	case "down":
		if m.selected+1 < len(m.visibleApplications()) {
			m.selected++
		}
	case "/":
		m.screen = screenAvailable
		m.searching = true
		m.query = ""
		m.selected = 0
	case "i":
		m.screen = screenInstalled
		m.selected = 0
	case "u":
		m.screen = screenUpdates
		m.selected = 0
	case "esc":
		if m.screen == screenRollback {
			m.screen = m.returnTo
			if m.screen != screenDetails {
				m.detail = nil
			}
		} else if m.screen == screenDetails || m.screen == screenVersions {
			m.screen = m.returnTo
			if m.screen != screenDetails {
				m.detail = nil
			}
			m.versions = nil
		} else {
			m.screen = screenAvailable
			m.selected = 0
		}
	case "enter":
		if m.screen == screenRollback {
			if id := m.selectedID(); id != "" {
				m.busy = "Rolling back"
				return m, m.rollbackCmd(id)
			}
			return m, nil
		}
		if m.screen == screenDetails {
			return m.activateSelected()
		}
		visible := m.visibleApplications()
		if len(visible) != 0 {
			selected := visible[m.selected]
			m.detail = &selected
			m.returnTo = m.screen
			m.screen = screenDetails
		}
	case "v":
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.setDetail(id)
			m.returnTo = m.screen
			m.busy = "Loading versions"
			return m, m.versionsCmd(id)
		}
	case "r":
		if id := m.selectedID(); id != "" && m.selectedInstalled() {
			m.setDetail(id)
			m.returnTo = m.screen
			m.screen = screenRollback
		}
	}
	m.clampSelection()
	return m, nil
}

func (m model) activateSelected() (tea.Model, tea.Cmd) {
	if m.detail == nil {
		return m, nil
	}
	id := m.detail.ID
	switch {
	case m.detail.InstalledVersion == "":
		m.busy = "Installing " + id
		return m, m.installCmd(id)
	case m.detail.UpdateAvailable:
		m.busy = "Updating " + id
		return m, m.updateCmd(id)
	default:
		m.status = id + " is already current"
		return m, nil
	}
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

func (m model) searchCmd() tea.Cmd {
	return func() tea.Msg {
		values, err := m.service.Search(m.ctx, m.query)
		return searchMsg{values: values, err: err}
	}
}

func (m model) versionsCmd(id string) tea.Cmd {
	return func() tea.Msg {
		values, err := m.service.Versions(m.ctx, id)
		return versionsMsg{values: values, err: err}
	}
}

func (m model) installCmd(id string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.service.Install(m.ctx, id, nil)
		return operationMsg{message: resultMessage("Installed", result), err: err}
	}
}

func (m model) updateCmd(id string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.service.Update(m.ctx, id, nil)
		return operationMsg{message: resultMessage("Updated", result), err: err}
	}
}

func (m model) rollbackCmd(id string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.service.Rollback(m.ctx, id, nil)
		return operationMsg{message: resultMessage("Rolled back", result), err: err, next: screenDetails, move: true}
	}
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
	for _, value := range m.available {
		if value.ID == m.detail.ID {
			copy := value
			m.detail = &copy
			return
		}
	}
}

func (m model) visibleApplications() []app.Application {
	switch m.screen {
	case screenInstalled:
		return m.installed
	case screenUpdates:
		return updates(m.installed)
	default:
		return m.available
	}
}

func (m model) selectedID() string {
	if m.detail != nil && (m.screen == screenDetails || m.screen == screenVersions || m.screen == screenRollback) {
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
}

func updates(values []app.Application) []app.Application {
	result := make([]app.Application, 0, len(values))
	for _, value := range values {
		if value.UpdateAvailable {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func writeApplications(destination *strings.Builder, values []app.Application, selected int) {
	if len(values) == 0 {
		destination.WriteString("No applications.\n")
		return
	}
	for index, value := range values {
		marker := "  "
		if index == selected {
			marker = "> "
		}
		fmt.Fprintf(destination, "%s%-20s %-12s %s\n", marker, value.Name, value.RegistryVersion, installedLabel(value))
	}
}

func installedLabel(value app.Application) string {
	if value.InstalledVersion == "" {
		return "Install"
	}
	if value.UpdateAvailable {
		return value.InstalledVersion + " → Update"
	}
	return value.InstalledVersion + " current"
}

func resultMessage(action string, result app.Result) string {
	if result.AppID == "" {
		return ""
	}
	return fmt.Sprintf("%s %s %s", action, result.AppID, result.Version)
}
