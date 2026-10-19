package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

func navigationApplications() (available, installed []app.Application) {
	installed = []app.Application{
		{ID: "one", Name: "One", InstalledVersion: "1.0", RegistryVersion: "2.0", UpdateAvailable: true},
	}
	available = []app.Application{
		installed[0],
		{ID: "two", Name: "Two"},
	}
	return available, installed
}

func underlinedMnemonic(value, mnemonic string) bool {
	index := strings.Index(value, mnemonic)
	if index < 0 {
		return false
	}
	start := strings.LastIndex(value[:index], "\x1b[")
	if start < 0 {
		return false
	}
	sequence := value[start+2:]
	end := strings.Index(sequence, "m")
	if end < 0 {
		return false
	}
	for _, parameter := range strings.Split(sequence[:end], ";") {
		if parameter == "4" {
			return true
		}
	}
	return false
}

func TestUnifiedTopBarCountsAndRemovesSecondaryFilterRow(t *testing.T) {
	available, installed := navigationApplications()
	m := model{screen: screenAvailable, available: available, installed: installed, dataLoaded: true, width: 100, height: 24, theme: newTheme(false)}
	view := m.View().Content
	plainView := ansi.Strip(view)
	for _, want := range []string{"Browse", "Installed 1", "Not installed", "Updates 1"} {
		if !strings.Contains(plainView, want) {
			t.Fatalf("top bar missing %q: %q", want, view)
		}
	}
	if strings.Contains(plainView, "[ All ]") || strings.Contains(plainView, "←/→ Filter") {
		t.Fatalf("secondary filter row remains: %q", view)
	}
	if strings.Contains(plainView, "Applications\n") {
		t.Fatalf("redundant Applications heading remains: %q", view)
	}
	if strings.Count(ansi.Strip(view), "Browse") != 1 {
		t.Fatalf("navigation was duplicated: %q", view)
	}
}

func TestTopBarMarksTheActiveTab(t *testing.T) {
	_, installed := navigationApplications()
	for _, test := range []struct {
		screen screen
		want   string
	}{
		{screenAvailable, "[ Browse ]"},
		{screenInstalled, "[ Installed 1 ]"},
		{screenNotInstalled, "[ Not installed ]"},
		{screenUpdates, "[ Updates 1 ]"},
	} {
		m := model{screen: test.screen, installed: installed, dataLoaded: true, width: 100, theme: newTheme(false)}
		if !strings.Contains(ansi.Strip(m.tabsLine()), test.want) {
			t.Fatalf("screen %d active tab missing %q: %q", test.screen, test.want, m.tabsLine())
		}
	}
}

func TestTopBarUnderlinesMnemonicsEvenWhenInactive(t *testing.T) {
	_, installed := navigationApplications()
	for _, color := range []bool{false, true} {
		m := model{screen: screenNotInstalled, installed: installed, dataLoaded: true, width: 120, color: color, theme: newTheme(color)}
		line := m.tabsLine()
		for mnemonic, label := range map[string]string{"B": "Browse", "I": "Installed", "N": "Not installed", "U": "Updates"} {
			if !strings.Contains(ansi.Strip(line), label) {
				t.Fatalf("color=%v top bar missing %q: %q", color, label, line)
			}
			if !underlinedMnemonic(line, mnemonic) {
				t.Fatalf("color=%v mnemonic %q is not ANSI-underlined: %q", color, mnemonic, line)
			}
		}
		for width := 30; width <= 120; width++ {
			m.width = width
			rendered := m.tabsLine()
			for _, row := range strings.Split(rendered, "\n") {
				if ansi.StringWidth(row) > width {
					t.Fatalf("top bar exceeds width %d: %q", width, rendered)
				}
			}
			plain := ansi.Strip(rendered)
			for _, label := range []string{"Browse", "Installed 1", "Not installed", "Updates 1"} {
				if !strings.Contains(plain, label) {
					t.Fatalf("width %d hides %q: %q", width, label, rendered)
				}
			}
			m.screen = screenUpdates
			if !strings.Contains(ansi.Strip(m.tabsLine()), "[ Updates 1 ]") {
				t.Fatalf("width %d hides active Updates tab: %q", width, m.tabsLine())
			}
			m.screen = screenNotInstalled
		}
	}
}

func TestEmptyStateRemainsSpecificToEachTopLevelTab(t *testing.T) {
	for _, test := range []struct {
		screen screen
		want   string
	}{
		{screenAvailable, `No matches for "needle".`},
		{screenInstalled, "No installed applications."},
		{screenNotInstalled, `No matches for "needle".`},
		{screenUpdates, "No updates available."},
	} {
		m := model{screen: test.screen, dataLoaded: true, query: "needle", width: 100, height: 24, theme: newTheme(false)}
		if got := ansi.Strip(m.View().Content); !strings.Contains(got, test.want) {
			t.Errorf("screen %d empty state missing %q: %q", test.screen, test.want, got)
		}
	}
}

func TestMnemonicKeysSwitchTabsCaseInsensitively(t *testing.T) {
	available, installed := navigationApplications()
	for _, test := range []struct {
		from screen
		key  string
		want screen
	}{
		{screenInstalled, "b", screenAvailable},
		{screenInstalled, "B", screenAvailable},
		{screenAvailable, "i", screenInstalled},
		{screenAvailable, "I", screenInstalled},
		{screenAvailable, "n", screenNotInstalled},
		{screenAvailable, "N", screenNotInstalled},
		{screenAvailable, "u", screenUpdates},
		{screenAvailable, "U", screenUpdates},
	} {
		m := model{screen: test.from, available: available, installed: installed, dataLoaded: true, width: 80, height: 24}
		updated, _ := m.Update(key(test.key))
		if got := updated.(model).screen; got != test.want {
			t.Fatalf("screen %d + %q = screen %d, want %d", test.from, test.key, got, test.want)
		}
	}
}

func TestArrowKeysCycleTopLevelTabs(t *testing.T) {
	available, installed := navigationApplications()
	for _, test := range []struct {
		from screen
		key  rune
		want screen
	}{
		{screenAvailable, tea.KeyLeft, screenUpdates},
		{screenAvailable, tea.KeyRight, screenInstalled},
		{screenInstalled, tea.KeyLeft, screenAvailable},
		{screenInstalled, tea.KeyRight, screenNotInstalled},
		{screenNotInstalled, tea.KeyLeft, screenInstalled},
		{screenNotInstalled, tea.KeyRight, screenUpdates},
		{screenUpdates, tea.KeyLeft, screenNotInstalled},
		{screenUpdates, tea.KeyRight, screenAvailable},
	} {
		m := model{screen: test.from, available: available, installed: installed, dataLoaded: true, width: 80, height: 24}
		updated, _ := m.Update(tea.KeyPressMsg{Code: test.key})
		got := updated.(model)
		if got.screen != test.want {
			t.Errorf("screen %d + key %v = screen %d, want %d", test.from, test.key, got.screen, test.want)
		}
	}
}

func TestArrowTabHintAndInputPrecedence(t *testing.T) {
	m := model{screen: screenAvailable, width: 100, height: 24}
	if help := strings.Join(m.helpOverlayLines(), "\n"); !strings.Contains(help, "←/→ Tabs") {
		t.Fatalf("arrow tab shortcut missing from Help: %q", help)
	}
	if help := m.helpView(); !strings.Contains(help, "←/→ Tabs") {
		t.Fatalf("arrow tab shortcut missing from footer: %q", help)
	}

	searching := model{screen: screenAvailable, searching: true, width: 80, height: 24}
	searching.initComponents()
	searching.searchInput.SetValue("query")
	searching.searchInput.Focus()
	updated, _ := searching.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	got := updated.(model)
	if !got.searching || got.screen != screenAvailable || got.searchInput.Value() != "query" {
		t.Fatalf("right arrow escaped search input: screen=%d searching=%v query=%q", got.screen, got.searching, got.searchInput.Value())
	}

	for _, state := range []model{
		{screen: screenAvailable, helpOverlay: true, width: 80, height: 24},
		{screen: screenAvailable, busy: "Working", width: 80, height: 24},
		{screen: screenUninstall, width: 80, height: 24},
	} {
		updated, _ := state.Update(tea.KeyPressMsg{Code: tea.KeyRight})
		got := updated.(model)
		if got.screen != state.screen || got.helpOverlay != state.helpOverlay {
			t.Errorf("right arrow escaped focused state %#v: screen=%d help=%v", state, got.screen, got.helpOverlay)
		}
	}
}

func TestSearchInputKeepsMnemonicKeys(t *testing.T) {
	available, installed := navigationApplications()
	for _, text := range []string{"b", "I", "n", "U"} {
		m := model{screen: screenAvailable, available: available, installed: installed, searching: true, searchFrom: screenAvailable, width: 80, height: 24}
		m.initComponents()
		m.searchInput.Focus()
		updated, _ := m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
		got := updated.(model)
		if !got.searching || got.screen != screenAvailable || got.searchInput.Value() != text {
			t.Fatalf("search swallowed %q: searching=%v screen=%d value=%q", text, got.searching, got.screen, got.searchInput.Value())
		}
	}
}

func TestDialogsIgnoreMnemonicTabKeys(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", InstalledVersion: "1.0"}
	for _, text := range []string{"b", "I", "n", "U", "g"} {
		m := model{screen: screenUninstall, detail: &value, upgradeAvailable: true, width: 80, height: 24}
		updated, _ := m.Update(tea.KeyPressMsg{Code: rune(text[0]), Text: text})
		got := updated.(model)
		if got.screen != screenUninstall || got.searching || got.helpOverlay {
			t.Fatalf("dialog key %q escaped: screen=%d searching=%v help=%v", text, got.screen, got.searching, got.helpOverlay)
		}
	}
}

func TestUpgradeUsesAlternateKeyWhileUppercaseUNavigates(t *testing.T) {
	available, installed := navigationApplications()
	newModel := func() model {
		return model{
			screen: screenAvailable, available: available, installed: installed, dataLoaded: true,
			upgradeAvailable: true, tarlinkVersion: app.TarLinkVersion{Current: "0.15.3", Latest: "0.15.4"},
			width: 80, height: 24,
		}
	}
	for _, text := range []string{"u", "U"} {
		updated, _ := newModel().Update(key(text))
		if got := updated.(model).screen; got != screenUpdates {
			t.Fatalf("%q opened screen %d, want Updates", text, got)
		}
	}
	updated, _ := newModel().Update(key("g"))
	if got := updated.(model).screen; got != screenUpgrade {
		t.Fatalf("g opened screen %d, want Upgrade", got)
	}
	help := strings.Join(strings.Fields(strings.Join(newModel().helpOverlayLines(), "\n")), " ")
	if !strings.Contains(help, "g Upgrade") || strings.Contains(help, "U Upgrade") {
		t.Fatalf("contextual help does not reflect the remapped upgrade key: %q", help)
	}
}

func TestNotInstalledListFiltersAndKeepsStableCursor(t *testing.T) {
	values := []app.Application{
		{ID: "installed", Name: "Installed", InstalledVersion: "1.0"},
		{ID: "free", Name: "Free"},
		{ID: "other", Name: "Other"},
	}
	m := model{screen: screenAvailable, available: values, installed: values[:1], dataLoaded: true, selected: 2, cursorID: "other", width: 100, height: 24, theme: newTheme(false)}
	updated, _ := m.Update(key("n"))
	m = updated.(model)
	if m.screen != screenNotInstalled || m.selectedID() != "other" {
		t.Fatalf("not-installed tab screen=%d selected=%q", m.screen, m.selectedID())
	}
	visible := m.visibleApplications()
	if len(visible) != 2 || visible[0].ID != "free" || visible[1].ID != "other" {
		t.Fatalf("not-installed list = %#v", visible)
	}
	updated, _ = m.Update(key("i"))
	m = updated.(model)
	if m.selected != 0 || m.selectedID() != "installed" {
		t.Fatalf("installed tab did not clamp selection: selected=%d id=%q", m.selected, m.selectedID())
	}
}

func TestTabSwitchClampsSelectionAndClearsStaleState(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "One", InstalledVersion: "1.0"},
		{ID: "two", Name: "Two"},
		{ID: "three", Name: "Three"},
	}
	stale := app.Application{ID: "stale", Name: "Stale"}
	m := model{
		screen: screenAvailable, available: values, installed: values[:1], dataLoaded: true,
		selected: 2, cursorID: "missing", detail: &stale, selectedIDs: map[string]bool{"two": true},
		width: 100, height: 24, theme: newTheme(false),
	}
	updated, _ := m.Update(key("i"))
	got := updated.(model)
	if got.screen != screenInstalled || got.selected != 0 || got.selectedID() != "one" {
		t.Fatalf("installed tab selection: screen=%d selected=%d id=%q", got.screen, got.selected, got.selectedID())
	}
	if got.detail != nil || len(got.selectedIDs) != 0 {
		t.Fatalf("stale detail or selection survived: detail=%#v selectedIDs=%#v", got.detail, got.selectedIDs)
	}
}

func TestNotInstalledEmptyState(t *testing.T) {
	installed := []app.Application{{ID: "one", Name: "Alpha", InstalledVersion: "1.0"}}
	m := model{screen: screenAvailable, available: installed, installed: installed, dataLoaded: true, width: 100, height: 24, theme: newTheme(false)}
	updated, _ := m.Update(key("n"))
	got := updated.(model)
	if got.screen != screenNotInstalled || got.selected != 0 {
		t.Fatalf("empty tab selection: screen=%d selected=%d", got.screen, got.selected)
	}
	view := got.View().Content
	if !strings.Contains(view, "No applications available to install.") || strings.Contains(view, "Alpha") {
		t.Fatalf("empty not-installed view = %q", view)
	}
}

func TestTabRoundTripKeepsRestoredCursorVisible(t *testing.T) {
	available := make([]app.Application, 30)
	for index := range available {
		available[index] = app.Application{ID: fmt.Sprintf("app-%02d", index), Name: fmt.Sprintf("App %02d", index)}
	}
	available[len(available)-1].InstalledVersion = "1.0"
	available[len(available)-1].RegistryVersion = "2.0"
	available[len(available)-1].UpdateAvailable = true
	m := model{
		screen: screenAvailable, available: available, installed: []app.Application{available[len(available)-1]},
		selected: len(available) - 1, cursorID: available[len(available)-1].ID,
		dataLoaded: true, width: 80, height: 10, theme: newTheme(false),
	}
	for _, keyText := range []string{"u", "b"} {
		updated, _ := m.Update(key(keyText))
		m = updated.(model)
	}
	if m.selectedID() != available[len(available)-1].ID {
		t.Fatalf("restored selected ID = %q", m.selectedID())
	}
	for _, row := range strings.Split(ansi.Strip(m.applicationTable.View()), "\n") {
		if strings.Contains(row, "App 29") {
			if !strings.Contains(row, ">") {
				t.Fatalf("restored selection is not visible in its row: %q", row)
			}
			return
		}
	}
	t.Fatalf("restored cursor row is not visible: %q", m.applicationTable.View())
}

func TestNotInstalledKeepsBatchInstallBehavior(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	service := &fakeService{available: values, resolved: []app.BatchTarget{{AppID: "one", Name: "One", Version: "1.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenNotInstalled, available: values, dataLoaded: true, width: 100, height: 24}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = updated.(model)
	if !m.selectedIDs["one"] {
		t.Fatalf("not-installed list ignored Space: %#v", m.selectedIDs)
	}
	updated, _ = m.Update(key("enter"))
	m = updated.(model)
	if m.screen != screenDetails || m.returnTo != screenNotInstalled {
		t.Fatalf("review target: screen=%d returnTo=%d", m.screen, m.returnTo)
	}
	updated, command := m.Update(key("enter"))
	m = updated.(model)
	if command == nil || m.busy != "Resolving selected applications" {
		t.Fatalf("selected batch did not resolve: busy=%q command=%v", m.busy, command)
	}
}
