package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

func listFixture(count int) []app.Application {
	values := make([]app.Application, count)
	for i := range values {
		values[i] = app.Application{ID: fmt.Sprintf("app-%04d", i), Name: fmt.Sprintf("Application %04d", i)}
	}
	return values
}

// noColorSelectionVisible reports whether the rendered view marks the named
// application as the cursor row. Without color, the marker is the only cursor
// indicator, so a hidden marker means the selection highlight disappeared.
func noColorSelectionVisible(view string, name string) bool {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, ">") && strings.Contains(line, name) {
			return true
		}
	}
	return false
}

func TestListSelectionRemainsVisibleWhileScrollingDown(t *testing.T) {
	values := listFixture(30)
	m := model{screen: screenAvailable, available: values, dataLoaded: true, width: 80, height: 15, theme: newTheme(false)}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m = updated.(model)
	for index, value := range values {
		if index > 0 {
			updated, _ := m.Update(key("down"))
			m = updated.(model)
		}
		view := ansi.Strip(m.View().Content)
		if !noColorSelectionVisible(view, value.Name) {
			t.Fatalf("selected row %d (%q) not visible: selected=%d view=%q", index, value.Name, m.selected, view)
		}
	}
}

func TestListSelectionRemainsVisibleWhileScrollingUp(t *testing.T) {
	values := listFixture(30)
	m := model{screen: screenAvailable, available: values, dataLoaded: true, width: 80, height: 15, theme: newTheme(false)}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m = updated.(model)
	for range values[1:] {
		updated, _ := m.Update(key("down"))
		m = updated.(model)
	}
	for index := len(values) - 1; index >= 0; index-- {
		view := ansi.Strip(m.View().Content)
		if !noColorSelectionVisible(view, values[index].Name) {
			t.Fatalf("selected row %d (%q) not visible while scrolling up: selected=%d view=%q", index, values[index].Name, m.selected, view)
		}
		if index > 0 {
			updated, _ := m.Update(key("up"))
			m = updated.(model)
		}
	}
}

func TestListSelectionHighlightRemainsVisibleInColorMode(t *testing.T) {
	values := listFixture(30)
	m := model{screen: screenAvailable, available: values, dataLoaded: true, width: 80, height: 15, color: true, theme: newTheme(true)}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m = updated.(model)
	for range values[1:] {
		updated, _ := m.Update(key("down"))
		m = updated.(model)
	}
	// Only the focused row renders with the selection style. That styled row
	// must survive into the rendered view instead of being scrolled out of
	// the table viewport during rendering.
	highlighted := ""
	for _, line := range strings.Split(m.applicationTable.View(), "\n") {
		if strings.Contains(line, values[len(values)-1].Name) && strings.Contains(line, "\x1b[") {
			highlighted = line
		}
	}
	if highlighted == "" {
		t.Fatalf("bottom selected row is not highlighted: selected=%d table=%q", m.selected, m.applicationTable.View())
	}
	if !strings.Contains(m.View().Content, highlighted) {
		t.Fatalf("highlighted bottom row is not rendered: selected=%d highlighted=%q view=%q", m.selected, highlighted, m.View().Content)
	}
}

func TestListSelectionStaysVisibleAfterTableRebuild(t *testing.T) {
	values := listFixture(30)
	m := model{screen: screenAvailable, available: values, dataLoaded: true, width: 80, height: 15, theme: newTheme(false)}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m = updated.(model)
	for range values[1:] {
		updated, _ := m.Update(key("down"))
		m = updated.(model)
	}
	// Non-navigation keys and resizes rebuild the table; the selected row
	// must stay visible through them.
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = updated.(model)
	view := ansi.Strip(m.View().Content)
	if !noColorSelectionVisible(view, values[len(values)-1].Name) {
		t.Fatalf("selected row lost after selection rebuild: %q", view)
	}
	if !m.selectedIDs[values[len(values)-1].ID] {
		t.Fatalf("space did not select the bottom row: selectedIDs=%v", m.selectedIDs)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height - 2})
	m = updated.(model)
	view = ansi.Strip(m.View().Content)
	if !noColorSelectionVisible(view, values[len(values)-1].Name) {
		t.Fatalf("selected row lost after resize rebuild: %q", view)
	}
}

func TestListNavigationReusesConfiguredRows(t *testing.T) {
	values := listFixture(1000)
	m := model{screen: screenAvailable, available: values, dataLoaded: true, width: 80, height: 15, theme: newTheme(false)}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	m = updated.(model)
	updated, _ = m.Update(key("down"))
	m = updated.(model)
	before := m.applicationTable.Rows()
	updated, _ = m.Update(key("down"))
	m = updated.(model)
	after := m.applicationTable.Rows()
	if len(before) == 0 || len(after) == 0 || &before[0][0] != &after[0][0] {
		t.Fatal("vertical navigation rebuilt the application table rows")
	}
}
