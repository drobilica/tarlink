package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/drobilica/tarlink/internal/app"
)

type fakeService struct {
	available    []app.Application
	installed    []app.Application
	resolved     []app.BatchTarget
	installedIDs []string
}

func (f *fakeService) Install(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{AppID: "one", Version: "1.0"}, nil
}
func (f *fakeService) Update(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, nil
}
func (f *fakeService) UpdateAll(context.Context, app.ProgressSink) (app.UpdateAllResult, error) {
	return app.UpdateAllResult{}, nil
}
func (f *fakeService) Uninstall(context.Context, string, app.ProgressSink) error { return nil }
func (f *fakeService) UninstallAll(context.Context, app.ProgressSink) error      { return nil }
func (f *fakeService) Rollback(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, nil
}
func (f *fakeService) List(context.Context) ([]app.Application, error) { return f.installed, nil }
func (f *fakeService) ListAvailable(context.Context) ([]app.Application, error) {
	return f.available, nil
}
func (f *fakeService) Info(context.Context, string) (app.Application, error) {
	return app.Application{}, nil
}
func (f *fakeService) Search(context.Context, string) ([]app.Application, error) {
	return f.available, nil
}
func (f *fakeService) Versions(context.Context, string) ([]app.Version, error) { return nil, nil }
func (f *fakeService) SyncRegistry(context.Context, app.ProgressSink) error    { return nil }
func (f *fakeService) ValidateRegistry(context.Context, string) error          { return nil }
func (f *fakeService) CheckTarLinkVersion(context.Context) (app.TarLinkVersion, error) {
	return app.TarLinkVersion{}, nil
}
func (f *fakeService) CheckTarLinkVersionFresh(context.Context) (app.TarLinkVersion, error) {
	return app.TarLinkVersion{}, nil
}
func (f *fakeService) UpgradeTarLink(context.Context, app.ProgressSink) (app.TarLinkVersion, error) {
	return app.TarLinkVersion{}, nil
}
func (f *fakeService) CheckInstallPath(string) ([]app.PathConflict, error) { return nil, nil }
func (f *fakeService) Doctor(context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}
func (f *fakeService) ResolveInstallBatch(context.Context, []string) ([]app.BatchTarget, error) {
	return f.resolved, nil
}
func (f *fakeService) InstallBatch(_ context.Context, ids []string, _ app.ProgressSink) (app.BatchResult, error) {
	f.installedIDs = append([]string(nil), ids...)
	return app.BatchResult{}, nil
}
func (f *fakeService) UninstallBatch(context.Context, []string, app.ProgressSink) (app.BatchResult, error) {
	return app.BatchResult{}, nil
}

func key(text string) tea.KeyPressMsg { return tea.KeyPressMsg{Text: text} }

func TestTableSelectionReviewFlow(t *testing.T) {
	service := &fakeService{available: []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.available, width: 80, height: 24}
	updated, command := m.Update(key("down"))
	if command != nil || updated.(model).selected != 1 {
		t.Fatalf("down selected=%d command=%v", updated.(model).selected, command)
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if command != nil || len(updated.(model).selectedIDs) != 1 || !updated.(model).selectedIDs["two"] {
		t.Fatalf("space selection=%#v command=%v", updated.(model).selectedIDs, command)
	}
	updated, command = updated.(model).Update(key("enter"))
	got := updated.(model)
	if command != nil || got.screen != screenDetails || got.detail != nil || !strings.Contains(got.View().Content, "Review selection") {
		t.Fatalf("enter review = screen %v detail %#v command %v", got.screen, got.detail, command)
	}
}

func TestTableHasStablePackageManagerColumns(t *testing.T) {
	values := []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.1", RegistryVersion: "5.2", UpdateAvailable: true, DefaultChannel: "stable"}}
	m := model{screen: screenAvailable, available: values, width: 120, height: 20}
	view := m.View().Content
	for _, header := range []string{"APPLICATION", "STATUS", "INSTALLED", "AVAILABLE", "CHANNEL"} {
		if !strings.Contains(view, header) {
			t.Fatalf("table header %q missing from %q", header, view)
		}
	}
	if !strings.Contains(view, "UPDATE") || !strings.Contains(view, "5.1") || !strings.Contains(view, "5.2") {
		t.Fatalf("table data missing from %q", view)
	}
}

func TestApplicationTableColumnsStayAlignedAtTerminalWidths(t *testing.T) {
	values := []app.Application{{
		ID: "blender", Name: "A very long application name that must truncate", InstalledVersion: "1.5.5-appimage",
		RegistryVersion: "1.5.6-appimage", DefaultChannel: "stable", UpdateAvailable: true,
	}}
	for _, width := range []int{60, 80, 100, 120, 160} {
		m := model{screen: screenAvailable, available: values, width: width, height: 20}
		m.initComponents()
		m.configureApplicationTable(values, width-4, 12)
		view := m.applicationTable.View()
		lines := strings.Split(view, "\n")
		if len(lines) < 2 {
			t.Fatalf("width %d produced only %d table lines", width, len(lines))
		}
		assertTableLineAligned(t, width, m.applicationTable.Columns(), lines[0], lines[1])
	}
}

func TestApplicationTableResizePreservesLayoutAndCursor(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "A very long application name that must truncate", InstalledVersion: "1.5.5-appimage", RegistryVersion: "1.5.6-appimage", DefaultChannel: "stable"},
		{ID: "two", Name: "Second application", InstalledVersion: "—", RegistryVersion: "1.0.0", DefaultChannel: "stable"},
	}
	m := model{screen: screenAvailable, available: values, cursorID: "two", width: 120, height: 20}
	m.initComponents()
	for _, width := range []int{120, 80, 160, 60, 120} {
		m.width = width
		m.configureApplicationTable(values, width-4, 12)
		if got := m.applicationTable.Cursor(); got != 1 {
			t.Fatalf("width %d moved cursor to %d", width, got)
		}
		lines := strings.Split(m.applicationTable.View(), "\n")
		if len(lines) < 3 {
			t.Fatalf("width %d rendered too few lines", width)
		}
		assertTableLineAligned(t, width, m.applicationTable.Columns(), lines[0], lines[2])
	}
}

func assertTableLineAligned(t *testing.T, terminalWidth int, columns []table.Column, header, row string) {
	t.Helper()
	visibleHeader, visibleRow := ansi.Strip(header), ansi.Strip(row)
	if ansi.StringWidth(visibleHeader) > terminalWidth-4 || ansi.StringWidth(visibleRow) > terminalWidth-4 {
		t.Fatalf("table line exceeds allocated width %d: header=%d row=%d", terminalWidth-4, ansi.StringWidth(visibleHeader), ansi.StringWidth(visibleRow))
	}
	offset := 0
	for index, column := range columns {
		if column.Width <= 0 {
			continue
		}
		contentStart := offset + applicationTableCellStyle.GetPaddingLeft()
		if column.Title != "" {
			if got := runeAtDisplayColumn(visibleHeader, contentStart); got == ' ' || got == 0 {
				t.Fatalf("column %d header does not start at display column %d: %q", index, contentStart, header)
			}
		}
		if index > 0 {
			if got := runeAtDisplayColumn(visibleRow, contentStart); got == ' ' || got == 0 {
				t.Fatalf("column %d row does not start at display column %d: %q", index, contentStart, row)
			}
		}
		offset += column.Width + applicationTableCellStyle.GetPaddingLeft() + applicationTableCellStyle.GetPaddingRight()
	}
}

func runeAtDisplayColumn(value string, column int) rune {
	position := 0
	for _, character := range value {
		if position >= column {
			return character
		}
		position += ansi.StringWidth(string(character))
	}
	return 0
}

func TestSelectionTracksApplicationIDAcrossFiltering(t *testing.T) {
	values := []app.Application{
		{ID: "available", Name: "Available"},
		{ID: "installed", Name: "Installed", InstalledVersion: "1.0"},
	}
	m := model{screen: screenAvailable, available: values, width: 100, height: 20}
	updated, _ := m.Update(key("down"))
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m = updated.(model)
	if !m.selectedIDs["installed"] {
		t.Fatalf("selection did not use stable ID: %#v", m.selectedIDs)
	}
	updated, _ = m.Update(key("left"))
	m = updated.(model)
	if !m.selectedIDs["installed"] || m.selectedIDs["available"] {
		t.Fatalf("filter transferred selection: %#v", m.selectedIDs)
	}
}

func TestReviewAppliesResolvedBatch(t *testing.T) {
	service := &fakeService{available: []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}, resolved: []app.BatchTarget{{AppID: "one", Name: "One", Version: "1.0"}, {AppID: "two", Name: "Two", Version: "2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.available, selectedIDs: map[string]bool{"one": true, "two": true}, returnTo: screenAvailable}
	updated, command := m.Update(key("enter"))
	if command != nil || updated.(model).screen != screenDetails {
		t.Fatalf("review = %#v command=%v", updated, command)
	}
	updated, command = updated.(model).Update(key("enter"))
	if command == nil || updated.(model).busy != "Resolving selected applications" {
		t.Fatalf("resolution = %#v command=%v", updated, command)
	}
	updated, command = updated.(model).Update(command())
	got := updated.(model)
	if command != nil || got.screen != screenDetails || len(got.batchTargets) != 2 {
		t.Fatalf("resolved review = %#v command=%v", got, command)
	}
	updated, command = got.Update(key("enter"))
	if command == nil || updated.(model).busy != "Installing selected applications" {
		t.Fatalf("apply = %#v command=%v", updated, command)
	}
	message := command().(operationMsg)
	if message.err != nil || len(service.installedIDs) != 2 || service.installedIDs[0] != "one" {
		t.Fatalf("batch result=%#v IDs=%#v", message, service.installedIDs)
	}
}

func TestBatchReviewShowsFrozenResolvedVersions(t *testing.T) {
	m := model{
		screen:       screenDetails,
		selectedIDs:  map[string]bool{"one": true},
		batchTargets: []app.BatchTarget{{AppID: "one", Name: "One", Version: "2.4.1", Channel: "stable"}},
		width:        80,
	}
	view := strings.Join(m.reviewLines(), "\n")
	if !strings.Contains(view, "2.4.1") || !strings.Contains(view, "stable") || !strings.Contains(view, "Versions locked") {
		t.Fatalf("frozen review missing: %q", view)
	}
}

func TestTableUsesCharmTableScrollingAndIgnoresMouse(t *testing.T) {
	values := make([]app.Application, 20)
	for i := range values {
		values[i] = app.Application{ID: string(rune('a' + i)), Name: "Application"}
	}
	m := model{screen: screenAvailable, available: values, width: 80, height: 12}
	updated, command := m.Update(tea.MouseClickMsg{X: 2, Y: 5, Button: tea.MouseLeft})
	if command != nil || updated.(model).selected != 0 {
		t.Fatalf("mouse changed selection: %#v", updated)
	}
	updated, command = updated.(model).Update(key("down"))
	if command != nil || updated.(model).applicationTable.Cursor() != 1 {
		t.Fatalf("cursor=%d command=%v", updated.(model).applicationTable.Cursor(), command)
	}
	if updated.(model).View().MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse mode=%v", updated.(model).View().MouseMode)
	}
}

func TestProgressViewUsesCurrentEvent(t *testing.T) {
	m := model{width: 80, color: false, theme: newTheme(false), progressBar: newProgress(false), progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 50, BytesTotal: 100}}
	if got := m.progressLine(); !strings.Contains(got, "Downloading") || !strings.Contains(got, "50%") {
		t.Fatalf("progress=%q", got)
	}
	updated, command := m.Update(progressMsg{event: app.Progress{Stage: app.ProgressVerifying, BytesDone: 1}})
	if command == nil || updated.(model).progress.Stage != app.ProgressVerifying {
		t.Fatalf("event=%#v command=%v", updated, command)
	}
}

func TestProgressEstimatorRejectsReset(t *testing.T) {
	var estimator speedEstimator
	now := time.Now()
	estimator.Add(now, 100)
	if got := estimator.Add(now.Add(time.Second), 200); got != 100 {
		t.Fatalf("speed=%v", got)
	}
	if got := estimator.Add(now.Add(2*time.Second), 10); got != 0 || estimator.Ready() {
		t.Fatalf("reset speed=%v ready=%v", got, estimator.Ready())
	}
}

var _ app.Service = (*fakeService)(nil)
var _ app.BatchService = (*fakeService)(nil)
