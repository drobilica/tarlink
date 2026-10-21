package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

func panelFixture() model {
	value := app.Application{ID: "fixture", Name: "Fixture", Summary: "An application", InstalledVersion: "1.0", RegistryVersion: "2.0", PreviousVersion: "0.9", InstalledChannel: "stable", UpdateAvailable: true, Categories: []string{"games"}, Homepage: "https://example.test"}
	return model{ctx: context.Background(), service: &fakeService{}, screen: screenDetails, returnTo: screenInstalled, detail: &value, installed: []app.Application{value}, available: []app.Application{value}, width: 80, height: 24, theme: newTheme(false)}
}

func TestDetailAdaptiveActionsAndStableGeometry(t *testing.T) {
	var headingRow int
	for i, state := range []string{"update", "current", "pinned", "not installed", "no previous", "missing metadata"} {
		m := panelFixture()
		switch state {
		case "current":
			m.detail.UpdateAvailable = false
		case "pinned":
			m.detail.Pinned = true
		case "not installed":
			m.detail.InstalledVersion = ""
			m.installed = nil
		case "no previous":
			m.detail.PreviousVersion = ""
		case "missing metadata":
			m.detail = &app.Application{ID: "fixture"}
			m.installed = nil
		}
		lines := m.reviewLines()
		text := strings.Join(lines, "\n")
		row := -1
		for n, line := range lines {
			if line == "ACTIONS" {
				row = n
			}
		}
		if i == 0 {
			headingRow = row
		}
		if row != headingRow {
			t.Fatalf("%s moved actions from %d to %d", state, headingRow, row)
		}
		actions := m.detailActions()
		want := "Review update"
		if state == "current" || state == "pinned" {
			want = "View versions"
		}
		if state == "not installed" || state == "missing metadata" {
			want = "Install"
		}
		if actions[0].label != want {
			t.Fatalf("%s: primary=%s", state, actions[0].label)
		}
		for _, action := range actions {
			if !strings.Contains(text, action.label) {
				t.Fatalf("%s: missing rendered action %s", state, action.label)
			}
			if action.id == actionRollback && (state == "no previous" || state == "not installed") {
				t.Fatalf("%s offered rollback", state)
			}
		}
		if strings.Contains(text, "Version 2.0 available") || strings.Contains(text, "https://") {
			t.Fatalf("unexpected banner/expanded metadata: %s", text)
		}
	}
}

func TestDetailKeyboardWorkflows(t *testing.T) {
	for _, shortcut := range []string{"v", "r", "x"} {
		m := panelFixture()
		want := map[string]screen{"v": screenDetails, "r": screenRollback, "x": screenUninstall}[shortcut]
		var selected contextualActionID
		switch shortcut {
		case "v":
			selected = actionVersions
		case "r":
			selected = actionRollback
		case "x":
			selected = actionUninstall
		}
		for i, action := range m.detailActions() {
			if action.id == selected {
				m.detailSelected = i
			}
		}
		next, cmd := m.Update(key("enter"))
		if next.(model).screen != want || (shortcut == "v" && cmd == nil) {
			t.Fatalf("menu %s failed", shortcut)
		}
		direct, directCmd := panelFixture().Update(key(shortcut))
		if direct.(model).screen != next.(model).screen || (directCmd == nil) != (cmd == nil) {
			t.Fatalf("shortcut %s differs from menu", shortcut)
		}
		if shortcut != "v" {
			if cmd != nil {
				t.Fatal("confirmation started operation")
			}
			back, _ := next.(model).Update(key("esc"))
			if back.(model).screen != screenDetails {
				t.Fatal("confirmation Esc lost details")
			}
			_, confirm := next.(model).Update(key("enter"))
			if confirm == nil {
				t.Fatal("confirmation did not start operation")
			}
		}
	}
	for _, binding := range []string{"b", "B", "i", "I", "n", "N", "u", "U"} {
		next, cmd := panelFixture().Update(key(binding))
		if next.(model).screen == screenDetails || cmd != nil {
			t.Fatalf("global navigation %s failed", binding)
		}
	}
	m := panelFixture()
	next, _ := m.Update(key("down"))
	m = next.(model)
	if m.detailCursor() != 1 || !strings.Contains(m.View().Content, "› View versions") {
		t.Fatal("Down failed to select versions")
	}
	next, _ = m.Update(key("up"))
	m = next.(model)
	if m.detailCursor() != 0 {
		t.Fatal("Up failed")
	}
	next, _ = m.Update(key("up"))
	if next.(model).detailActions()[next.(model).detailCursor()].id != actionUninstall {
		t.Fatal("selection did not wrap")
	}
}

func TestDetailInstallAndPackageInformation(t *testing.T) {
	m := panelFixture()
	m.detail.InstalledVersion = ""
	m.installed = nil
	next, cmd := m.Update(key("i"))
	if next.(model).screen != screenInstalled || cmd != nil {
		t.Fatal("i must navigate to Installed")
	}
	next, cmd = m.Update(key("enter"))
	if cmd == nil || !strings.Contains(next.(model).busy, "installation path") {
		t.Fatal("Install bypassed path check")
	}
	m.detail.ChannelHeads = map[string]string{"stable": "2.0", "preview": "3.0"}
	next, cmd = m.Update(key("enter"))
	if next.(model).screen != screenInstallChannel || cmd != nil {
		t.Fatal("Install bypassed channel selection")
	}
	m = panelFixture()
	for i, action := range m.detailActions() {
		if action.id == actionPackage {
			m.detailSelected = i
		}
	}
	next, cmd = m.Update(key("enter"))
	m = next.(model)
	if cmd != nil || !m.packageExpanded || !strings.Contains(strings.Join(m.reviewLines(), "\n"), "https://example.test") {
		t.Fatal("package information failed")
	}
	next, _ = m.Update(key("esc"))
	if next.(model).packageExpanded || next.(model).screen != screenDetails {
		t.Fatal("Esc must collapse package information first")
	}
}

func TestDetailGeometryResizeSelectionFeedbackAndMonochrome(t *testing.T) {
	for _, color := range []bool{false, true} {
		m := panelFixture()
		m.color = color
		m.theme = newTheme(color)
		m.detail.Name = strings.Repeat("界application", 15)
		m.detail.Summary = strings.Repeat("Long summary ", 30)
		for _, size := range [][2]int{{120, 40}, {80, 24}, {40, 18}, {30, 12}, {60, 10}, {100, 30}} {
			next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = next.(model)
			before := m.reviewLines()
			m.status = "Completed"
			m.err = errors.New(strings.Repeat("failure ", 20))
			m.feedbackAppID = m.detail.ID
			after := m.reviewLines()
			for i, line := range before {
				if line == "ACTIONS" && after[i] != line {
					t.Fatal("feedback moved actions")
				}
			}
			m.clearFeedback()
			for i := range m.detailActions() {
				m.detailSelected = i
				m.ensureDetailSelection()
				view := ansi.Strip(m.View().Content)
				rows := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
				if len(rows) != size[1] {
					t.Fatalf("%v: got %d rows", size, len(rows))
				}
				for _, row := range rows {
					if ansi.StringWidth(row) > size[0] {
						t.Fatalf("%v overflow: %q", size, row)
					}
				}
				if !strings.Contains(view, "› "+m.detailActions()[i].label) {
					t.Fatalf("%v selected action invisible:\n%s", size, view)
				}
				if !strings.Contains(rows[len(rows)-1], "Enter") {
					t.Fatalf("%v footer missing Enter: %s", size, rows[len(rows)-1])
				}
			}
		}
	}
}

func TestDetailRefreshAndScrollPolicy(t *testing.T) {
	m := panelFixture()
	m.initComponents()
	before := ansi.Strip(m.View().Content)
	value := *m.detail
	value.InstalledVersion = value.RegistryVersion
	value.UpdateAvailable = false
	next, _ := m.Update(loadedMsg{available: []app.Application{value}, installed: []app.Application{value}})
	m = next.(model)
	after := ansi.Strip(m.View().Content)
	if m.detailActions()[0].id != actionVersions || strings.Contains(after, "Review update") {
		t.Fatal("refresh retained stale update action")
	}
	row := func(frame, text string) int {
		for i, line := range strings.Split(frame, "\n") {
			if strings.Contains(line, text) {
				return i
			}
		}
		return -1
	}
	if row(before, "ACTIONS") != row(after, "ACTIONS") {
		t.Fatal("update completion moved action heading")
	}
	m.status = "Application is current"
	m.err = errors.New("fixture failure")
	if row(m.View().Content, "ACTIONS") != row(after, "ACTIONS") {
		t.Fatal("feedback moved rendered controls")
	}
	m.clearFeedback()
	m.height = 12
	cursor := m.detailCursor()
	next, _ = m.Update(key("pgdown"))
	m = next.(model)
	if m.reviewScroll == 0 || m.detailCursor() != cursor {
		t.Fatal("page scroll changed selection or failed to scroll")
	}
	if !strings.Contains(strings.Join(m.helpOverlayLines(), "\n"), "PgUp/PgDn") {
		t.Fatal("scroll keys absent from Help")
	}
	next, _ = m.Update(key("up"))
	m = next.(model)
	if !strings.Contains(m.View().Content, "› Uninstall") {
		t.Fatal("selection did not recover visible action after scrolling")
	}
	m.busy = "Installing"
	offset := m.reviewScroll
	next, _ = m.Update(key("pgdown"))
	if next.(model).reviewScroll != offset {
		t.Fatal("busy state accepted page scroll")
	}
}

func TestDetailControlPanelFrame(t *testing.T) {
	m := panelFixture()
	m.detail.Requirements = []string{"original-game-data"}
	frame := ansi.Strip(m.View().Content)
	for _, text := range []string{"Fixture", "UPDATE AVAILABLE", "INSTALLED", "LATEST", "2.0 ↑", "› Review update", "SELECTED ACTION", "Requires original game data", "Enter Choose"} {
		if !strings.Contains(frame, text) {
			t.Fatalf("missing %q:\n%s", text, frame)
		}
	}
	t.Log("80×24 control panel:\n" + frame)
}

func TestDetailResizePreservesVisibleSelection(t *testing.T) {
	m := panelFixture()
	m.height = 40
	m.detailSelected = len(m.detailActions()) - 1
	for _, size := range [][2]int{{40, 12}, {30, 12}, {100, 40}, {60, 10}, {80, 24}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(model)
		if !strings.Contains(m.View().Content, "› Uninstall") {
			t.Fatalf("resize %v hid selection:\n%s", size, m.View().Content)
		}
	}
}

func TestDetailResultsVisibleWithoutMovingActions(t *testing.T) {
	for _, failed := range []bool{false, true} {
		m := panelFixture()
		m.detail.Requirements = []string{"original-game-data"}
		before := strings.Split(ansi.Strip(m.View().Content), "\n")
		m.feedbackAppID = m.detail.ID
		m.status = "Completed fixture operation"
		want := m.status
		if failed {
			m.err = errors.New("Planning failed: fixture timeout. " + strings.Repeat("Long error detail ", 25))
			want = "Planning failed: fixture timeout."
		}
		frame := ansi.Strip(m.View().Content)
		if !strings.Contains(frame, want) {
			t.Fatalf("feedback hidden:\n%s", frame)
		}
		after := strings.Split(frame, "\n")
		for i, line := range before {
			if strings.Contains(line, "ACTIONS") && after[i] != line {
				t.Fatal("result moved action heading")
			}
		}
		if failed && !strings.Contains(strings.Join(m.reviewLines(), "\n"), m.err.Error()[:70]) {
			t.Fatal("full error text discarded")
		}
		next, _ := m.Update(key("down"))
		m = next.(model)
		if m.err != nil || m.status != "" || !strings.Contains(m.View().Content, "SELECTED ACTION") {
			t.Fatal("selecting action did not restore contextual description")
		}
	}
}
