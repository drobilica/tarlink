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

func TestRenderedShellGeometry(t *testing.T) {
	application := app.Application{ID: "wide", Name: "Éditeur 界 é very long application name", Summary: "A useful application with a long description that should wrap inside the workspace without hiding controls.", InstalledVersion: "5.2.0-long-version", RegistryVersion: "5.2.1-long-version", DefaultChannel: "stable", UpdateAvailable: true, Categories: []string{"graphics", "game-development"}, Homepage: "https://example.test/a/very/long/path/that/should/remain/reachable"}
	for _, size := range [][2]int{{120, 40}, {100, 30}, {80, 24}, {60, 18}, {29, 9}, {0, 0}} {
		for _, screen := range []screen{screenAvailable, screenInstalled, screenUpdates, screenDetails, screenVersions, screenUninstall} {
			m := model{screen: screen, returnTo: screenUpdates, available: []app.Application{application}, installed: []app.Application{application}, detail: &application, versions: []app.Version{{Version: application.InstalledVersion, Status: "installed", Channel: "stable"}}, dataLoaded: true, width: size[0], height: size[1], theme: newTheme(false)}
			content := strings.TrimSuffix(m.View().Content, "\n")
			if (size[0] == 80 || size[0] == 120) && (screen == screenAvailable || screen == screenDetails || screen == screenVersions) {
				t.Logf("rendered %d×%d screen %d:\n%s", size[0], size[1], screen, content)
			}
			lines := strings.Split(content, "\n")
			if size[1] > 0 && len(lines) > size[1] {
				t.Errorf("%d×%d screen %d has %d rows", size[0], size[1], screen, len(lines))
			}
			for row, line := range lines {
				if size[0] > 0 && ansi.StringWidth(ansi.Strip(line)) > size[0] {
					t.Errorf("%d×%d screen %d row %d exceeds width: %q", size[0], size[1], screen, row, line)
				}
			}
		}
	}
}

func TestAppFeedbackStaysWithMatchingDetails(t *testing.T) {
	one := app.Application{ID: "one", Name: "One"}
	two := app.Application{ID: "two", Name: "Two"}
	m := model{screen: screenDetails, detail: &one, feedbackAppID: "one", status: "Updated one", width: 80, height: 24, theme: newTheme(false)}
	if !strings.Contains(m.View().Content, "Updated one") {
		t.Fatal("matching feedback absent")
	}
	m.detail = &two
	if strings.Contains(m.View().Content, "Updated one") {
		t.Fatal("feedback attached to another app")
	}
}

func TestSearchPrintableHelpKeyStaysInInput(t *testing.T) {
	m := model{screen: screenAvailable, searching: true, width: 80, height: 24}
	m.initComponents()
	m.searchInput.Focus()
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	got := updated.(model)
	if got.helpOverlay || got.searchInput.Value() != "?" {
		t.Fatalf("search key escaped input: help=%v query=%q", got.helpOverlay, got.searchInput.Value())
	}
}

func TestNoColorSelectionHasVisibleMarkerWithoutANSI(t *testing.T) {
	m := model{screen: screenAvailable, available: []app.Application{{ID: "one", Name: "One"}}, dataLoaded: true, width: 80, height: 24, theme: newTheme(false)}
	view := m.View().Content
	if strings.Contains(view, "\x1b") || !strings.Contains(view, ">   One") {
		t.Fatalf("no-color selection=%q", view)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if !strings.Contains(updated.(model).View().Content, ">✓") {
		t.Fatal("selected cursor lost its selection marker")
	}
}

func TestExpandedHelpFitsAndKeepsSelection(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	for _, size := range [][2]int{{80, 24}, {60, 18}, {40, 12}} {
		m := model{screen: screenAvailable, available: values, dataLoaded: true, helpOverlay: true, selected: 1, cursorID: "two", width: size[0], height: size[1], theme: newTheme(false)}
		lines := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatalf("expanded Help overflow at %d×%d: %d lines", size[0], size[1], len(lines))
		}
		if m.selected != 1 || m.cursorID != "two" {
			t.Fatal("render changed selection")
		}
	}
}

func TestFilterKeepsHighlightedRowAndEnterTargetTogether(t *testing.T) {
	values := []app.Application{{ID: "a", Name: "A"}, {ID: "b", Name: "B", InstalledVersion: "1"}, {ID: "c", Name: "C", InstalledVersion: "1"}}
	m := model{screen: screenAvailable, available: values, installed: values[1:], dataLoaded: true, selected: 2, cursorID: "c", width: 80, height: 24, theme: newTheme(false)}
	updated, _ := m.Update(key("right"))
	m = updated.(model)
	if m.selected != 1 || m.applicationTable.Cursor() != 1 || m.selectedID() != "c" {
		t.Fatalf("filter selected index=%d cursor=%d id=%s", m.selected, m.applicationTable.Cursor(), m.selectedID())
	}
	updated, _ = m.Update(key("enter"))
	if got := updated.(model).detail; got == nil || got.ID != "c" {
		t.Fatalf("Enter target=%#v", got)
	}
}

func TestUnavailableDataDoesNotClaimEmptyCatalog(t *testing.T) {
	m := model{screen: screenAvailable, err: errors.New("registry unavailable"), width: 80, height: 24, theme: newTheme(false)}
	view := m.View().Content
	if !strings.Contains(view, "Application data unavailable") || strings.Contains(view, "No applications") {
		t.Fatalf("unavailable data rendered as empty: %q", view)
	}
}

func TestChannelInstallFeedbackUsesCanonicalAppID(t *testing.T) {
	m := model{ctx: context.Background(), service: &fakeService{}, detail: &app.Application{ID: "one", Name: "One"}, operationGeneration: 1}
	cmd, _ := m.installCmd("one@beta")
	message, ok := cmd().(operationMsg)
	if !ok || message.appID != "one" {
		t.Fatalf("feedback ID for selector: %#v", message)
	}
}

func TestSpaceSelectionIsEligibleOnlyOnBatchLists(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", InstalledVersion: "1", UpdateAvailable: true}
	space := tea.KeyPressMsg{Code: tea.KeySpace}
	for _, screen := range []screen{screenAvailable, screenInstalled} {
		m := model{screen: screen, available: []app.Application{value}, installed: []app.Application{value}, width: 80, height: 24}
		if !strings.Contains(strings.Join(m.helpOverlayLines(), "\n"), "Toggle") {
			t.Fatalf("screen %d hides Space selection", screen)
		}
		updated, _ := m.Update(space)
		if !updated.(model).selectedIDs["one"] {
			t.Fatalf("screen %d ignored Space", screen)
		}
	}
	m := model{screen: screenUpdates, installed: []app.Application{value}, width: 80, height: 24}
	updated, _ := m.Update(space)
	if len(updated.(model).selectedIDs) != 0 || strings.Contains(strings.Join(m.helpOverlayLines(), "\n"), "Toggle") {
		t.Fatal("Updates accepted unsupported batch selection")
	}
}

func TestLongConflictModalScrollKeepsDecisionReachable(t *testing.T) {
	conflicts := make([]app.PathConflict, 0, 12)
	for i := 0; i < 12; i++ {
		conflicts = append(conflicts, app.PathConflict{Type: "PATH", Directory: "/a/long/path/with/many/components/that/needs/wrapping", Candidate: "candidate"})
	}
	for _, size := range [][2]int{{60, 18}, {80, 24}} {
		m := model{screen: screenInstallConfirm, pathConflicts: conflicts, width: size[0], height: size[1], theme: newTheme(false)}
		if size[0] == 60 && !m.modalScrollable() {
			t.Fatal("long conflict modal did not enable scroll")
		}
		for i := 0; i < 100; i++ {
			updated, _ := m.Update(key("down"))
			m = updated.(model)
		}
		view := m.View().Content
		if (m.modalScrollable() && m.overlayScroll == 0) || !strings.Contains(view, "Enter Install anyway") || len(strings.Split(strings.TrimSuffix(view, "\n"), "\n")) > size[1] {
			t.Fatalf("modal scroll at %d×%d: offset=%d view=%q", size[0], size[1], m.overlayScroll, view)
		}
	}
}

func TestLeavingUninstallClearsConflictNotice(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", InstalledVersion: "1"}
	m := model{screen: screenUninstall, confirmSet: true, confirmTo: screenDetails, detail: &value, uninstallConflict: &app.UninstallConflict{Path: "/tmp/example"}, width: 80, height: 24}
	updated, _ := m.Update(key("esc"))
	got := updated.(model)
	if got.uninstallConflict != nil || strings.Contains(got.View().Content, "Conflicting integration") {
		t.Fatal("uninstall conflict leaked after leaving confirmation")
	}
}

func TestEmptyColorTableHasNoSelectedPlaceholder(t *testing.T) {
	m := model{screen: screenUpdates, dataLoaded: true, width: 80, height: 24, color: true, theme: newTheme(true)}
	view := m.View().Content
	if !strings.Contains(view, "No updates available") || strings.Contains(view, "\x1b[7m") {
		t.Fatalf("empty table has selected row: %q", view)
	}
	for _, binding := range m.actionBindings() {
		if binding.Help().Key == "↑↓" || binding.Help().Key == "Enter" {
			t.Fatalf("empty list advertises unavailable action: %s", binding.Help().Key)
		}
	}
}

func TestConfirmedOverlayShowsRunningProgress(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", InstalledVersion: "1"}
	for _, screen := range []screen{screenUninstall, screenRollback, screenUpgrade, screenInstallConfirm, screenUninstallConflictConfirm} {
		m := model{screen: screen, detail: &value, busy: "Working", progress: app.Progress{Stage: app.ProgressDownloading, AppID: "one", BytesDone: 5, BytesTotal: 10}, opCancel: func() {}, width: 80, height: 24, theme: newTheme(false)}
		view := m.View().Content
		if !strings.Contains(view, "Downloading") || strings.Contains(view, "╭") {
			t.Fatalf("screen %d hides progress behind modal: %q", screen, view)
		}
	}
}

func TestExpandedHelpOnModalStaysWithinTerminal(t *testing.T) {
	for _, size := range [][2]int{{40, 12}, {60, 18}} {
		m := model{screen: screenUninstall, detail: &app.Application{Name: "One"}, helpOverlay: true, width: size[0], height: size[1], theme: newTheme(false)}
		lines := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
		if len(lines) > size[1] {
			t.Fatalf("%d×%d modal Help overflow: %d", size[0], size[1], len(lines))
		}
	}
}

func TestConfirmedInstallLeavesQuestionAndPreservesBackTarget(t *testing.T) {
	value := app.Application{ID: "one", Name: "One"}
	m := model{ctx: context.Background(), service: &fakeService{}, screen: screenDetails, returnTo: screenInstalled, detail: &value, width: 80, height: 24}
	updated, _ := m.Update(pathCheckMsg{appID: "one", conflicts: []app.PathConflict{{Type: "PATH", Directory: "/tmp", Candidate: "one"}}})
	m = updated.(model)
	if m.screen != screenInstallConfirm || m.returnTo != screenInstalled {
		t.Fatal("PATH confirmation lost source screen")
	}
	updated, cmd := m.Update(key("enter"))
	m = updated.(model)
	if cmd == nil || m.screen != screenDetails || !strings.Contains(m.View().Content, "Installing") || strings.Contains(m.View().Content, "Install anyway?") {
		t.Fatalf("confirmed install retained question: %q", m.View().Content)
	}
	message := cmd()
	updated, _ = m.Update(message)
	m = updated.(model)
	if m.screen != screenDetails || m.returnTo != screenInstalled || strings.Contains(m.View().Content, "Install anyway?") {
		t.Fatalf("completed install retained modal: %q", m.View().Content)
	}
}

func TestSearchCancelRestoresSourceQueryAndSelection(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One", InstalledVersion: "1"}, {ID: "two", Name: "Two", InstalledVersion: "1"}}
	m := model{screen: screenInstalled, installed: values, available: values, query: "old", selected: 1, cursorID: "two", width: 80, height: 24}
	updated, _ := m.Update(key("/"))
	m = updated.(model)
	if !m.searching || m.searchInput.Value() != "old" {
		t.Fatal("search failed to preserve accepted query")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = updated.(model)
	updated, _ = m.Update(key("esc"))
	m = updated.(model)
	if m.searching || m.screen != screenInstalled || m.query != "old" || m.selectedID() != "two" {
		t.Fatalf("search cancel lost state: screen=%d query=%q selected=%q", m.screen, m.query, m.selectedID())
	}
}

func TestRepeatedResizeKeepsScreensWithinBounds(t *testing.T) {
	value := app.Application{ID: "one", Name: "One 界", InstalledVersion: "1.0", RegistryVersion: "2.0", UpdateAvailable: true, Summary: "A description with enough words to wrap on a compact screen.", Homepage: "https://example.test/long/path/that/needs/wrapping"}
	for _, variant := range []model{
		{screen: screenAvailable, available: []app.Application{value}, installed: []app.Application{value}, dataLoaded: true, searching: true},
		{screen: screenDetails, detail: &value, returnTo: screenAvailable},
		{screen: screenVersions, detail: &value, versions: []app.Version{{Version: "1.0", Channel: "stable", Status: "installed"}}},
		{screen: screenAvailable, available: []app.Application{value}, dataLoaded: true, helpOverlay: true},
		{screen: screenUninstall, detail: &value},
		{screen: screenUninstall, detail: &value, busy: "Uninstalling", progress: app.Progress{Stage: app.ProgressCleaning, AppID: "one"}, opCancel: func() {}},
	} {
		m := variant
		m.theme = newTheme(false)
		for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 18}, {100, 30}, {80, 24}} {
			updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = updated.(model)
			lines := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
			if len(lines) > size[1] {
				t.Fatalf("screen %d after resize %d×%d has %d rows", m.screen, size[0], size[1], len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(ansi.Strip(line)) > size[0] {
					t.Fatalf("screen %d after resize %d×%d exceeds width: %q", m.screen, size[0], size[1], line)
				}
			}
		}
	}
}

func TestListAggregateCountsAppearOnce(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", InstalledVersion: "1", RegistryVersion: "2", UpdateAvailable: true}
	m := model{screen: screenUpdates, installed: []app.Application{value}, dataLoaded: true, width: 80, height: 24}
	view := m.View().Content
	if strings.Count(view, "Installed 1") != 1 || strings.Count(view, "Updates 1") != 1 {
		t.Fatalf("aggregate counters duplicated: %q", view)
	}
}

func TestBusyHelpKeyFollowsActionPolicy(t *testing.T) {
	m := model{screen: screenDetails, busy: "Updating", opCancel: func() {}, detail: &app.Application{ID: "one"}, width: 80, height: 24}
	updated, _ := m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if updated.(model).helpOverlay {
		t.Fatal("busy state opened unavailable Help")
	}
}

func TestReviewScrollClampsAcrossTallShrinkGrow(t *testing.T) {
	value := app.Application{ID: "one", Name: "One", Summary: "Long details that remain reachable", Homepage: "https://example.test/long/path/to/inspect", InstalledVersion: "1", RegistryVersion: "2"}
	m := model{screen: screenDetails, detail: &value, width: 80, height: 40}
	for i := 0; i < 20; i++ {
		m.moveReviewScroll(1)
	}
	if m.reviewScroll != 0 {
		t.Fatalf("tall Details accumulated invisible offset: %d", m.reviewScroll)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 10})
	m = updated.(model)
	if !strings.Contains(m.View().Content, "One") {
		t.Fatal("shrink hid heading without scrolling")
	}
	m.moveReviewScroll(3)
	if m.reviewScroll == 0 {
		t.Fatal("small viewport did not scroll")
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	if got := updated.(model).reviewScroll; got != 0 {
		t.Fatalf("grow retained invisible offset: %d", got)
	}
}
