package tui

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/clipperhouse/displaywidth"
	"github.com/drobilica/tarlink/internal/app"
)

type fakeService struct {
	applications     []app.Application
	installedValues  []app.Application
	rolledBack       string
	installedID      string
	uninstalled      string
	uninstallErr     error
	installProgress  []app.Progress
	tarlinkVersion   app.TarLinkVersion
	blockOnCancel    bool
	installStarted   chan struct{}
	installCanceled  chan struct{}
	blockSearch      bool
	searchStarted    chan struct{}
	searchCanceled   chan struct{}
	blockVersions    bool
	versionsStarted  chan struct{}
	versionsCanceled chan struct{}
	pathConflicts    []app.PathConflict
}

// waitForCancel blocks until ctx is cancelled, signalling started/canceled via
// the provided channels (which may be nil).
func (f *fakeService) waitForCancel(ctx context.Context, started, canceled chan struct{}) error {
	if started != nil {
		close(started)
	}
	<-ctx.Done()
	if canceled != nil {
		close(canceled)
	}
	return ctx.Err()
}

func (f *fakeService) Install(ctx context.Context, id string, sink app.ProgressSink) (app.Result, error) {
	f.installedID = id
	for _, event := range f.installProgress {
		if sink != nil {
			sink(event)
		}
	}
	if f.blockOnCancel {
		if f.installStarted != nil {
			close(f.installStarted)
		}
		<-ctx.Done()
		if f.installCanceled != nil {
			close(f.installCanceled)
		}
		return app.Result{}, ctx.Err()
	}
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
}

func TestMouseRowClickOpensDetails(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	m := model{screen: screenAvailable, available: values, width: 80, height: 20}
	start, _ := m.listBounds()
	updated, command := m.Update(tea.MouseClickMsg{X: 2, Y: start + 1, Button: tea.MouseLeft})
	got := updated.(model)
	if command != nil || got.screen != screenDetails || got.detail == nil || got.detail.ID != "two" || got.selected != 1 {
		t.Fatalf("row click = screen %v detail %#v selected %d command %v", got.screen, got.detail, got.selected, command)
	}
}

func TestInstallChannelSelectorUsesDefaultAndExplicitChannel(t *testing.T) {
	service := &fakeService{}
	value := app.Application{ID: "pcsx2", Name: "PCSX2", ChannelHeads: map[string]string{"stable": "1", "nightly": "2"}, DefaultChannel: "stable"}
	m := model{ctx: context.Background(), service: service, screen: screenDetails, detail: &value}
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || updated.(model).screen != screenInstallChannel || updated.(model).channels[0] != "nightly" || updated.(model).channelSelected != 1 {
		t.Fatalf("channel chooser = %#v command %v", updated.(model), command)
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Text: "up"})
	if command != nil || updated.(model).channelSelected != 0 {
		t.Fatalf("channel move = %#v command %v", updated.(model), command)
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || updated.(model).busy != "Checking installation path" {
		t.Fatalf("channel confirmation did not start path check: busy=%q command=%v", updated.(model).busy, command)
	}
	if got := updated.(model).installSelector("pcsx2"); got != "pcsx2@nightly" {
		t.Fatalf("install selector = %q", got)
	}
}

func TestChannelChooserMouseSelectionAndStaleChannelProtection(t *testing.T) {
	value := app.Application{ID: "pcsx2", Name: "PCSX2", ChannelHeads: map[string]string{"stable": "1", "nightly": "2"}, DefaultChannel: "stable"}
	m := model{screen: screenInstallChannel, detail: &value, channels: []string{"nightly", "stable"}, channelSelected: 1, width: 80, height: 20}
	start, _ := m.channelBounds()
	lines := strings.Split(m.View().Content, "\n")
	if start >= len(lines) || !strings.Contains(lines[start], "nightly") {
		t.Fatalf("channel hit-test row %d does not match rendered view: %q", start, m.View().Content)
	}
	updated, command := m.Update(tea.MouseClickMsg{X: 2, Y: start, Button: tea.MouseLeft})
	if command != nil || updated.(model).channelSelected != 0 {
		t.Fatalf("channel click selected=%d command=%v", updated.(model).channelSelected, command)
	}
	stale := updated.(model)
	stale.detail = &app.Application{ID: "tiled", Name: "Tiled"}
	if got := stale.installSelector("tiled"); got != "tiled" {
		t.Fatalf("stale channel selector = %q", got)
	}
}
func (f *fakeService) Update(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
}
func (f *fakeService) UpdateAll(context.Context, app.ProgressSink) (app.UpdateAllResult, error) {
	return app.UpdateAllResult{}, errors.New("unused")
}
func (f *fakeService) Uninstall(_ context.Context, id string, _ app.ProgressSink) error {
	f.uninstalled = id
	if f.uninstallErr != nil {
		return f.uninstallErr
	}
	for index, value := range f.applications {
		if value.ID == id {
			f.applications[index].InstalledVersion = ""
			f.applications[index].PreviousVersion = ""
			f.applications[index].UpdateAvailable = false
		}
	}
	for index, value := range f.installedValues {
		if value.ID == id {
			f.installedValues = append(f.installedValues[:index], f.installedValues[index+1:]...)
			break
		}
	}
	return nil
}
func (f *fakeService) UninstallAll(context.Context, app.ProgressSink) error {
	return errors.New("unused")
}
func (f *fakeService) Rollback(_ context.Context, id string, _ app.ProgressSink) (app.Result, error) {
	f.rolledBack = id
	return app.Result{AppID: id, Version: "5.1.0"}, nil
}

func (f *fakeService) List(context.Context) ([]app.Application, error) {
	if f.installedValues != nil {
		return f.installedValues, nil
	}
	return f.applications, nil
}
func (f *fakeService) Info(context.Context, string) (app.Application, error) {
	return app.Application{}, errors.New("unused")
}
func (f *fakeService) Search(ctx context.Context, query string) ([]app.Application, error) {
	if f.blockSearch {
		return nil, f.waitForCancel(ctx, f.searchStarted, f.searchCanceled)
	}
	if query == "" {
		return f.applications, nil
	}
	values := make([]app.Application, 0, len(f.applications))
	for _, value := range f.applications {
		if strings.Contains(strings.ToLower(value.ID), strings.ToLower(query)) || strings.Contains(strings.ToLower(value.Name), strings.ToLower(query)) {
			values = append(values, value)
		}
	}
	return values, nil
}
func (f *fakeService) Versions(ctx context.Context, _ string) ([]app.Version, error) {
	if f.blockVersions {
		return nil, f.waitForCancel(ctx, f.versionsStarted, f.versionsCanceled)
	}
	return []app.Version{{Version: "5.2.0", Status: "current"}}, nil
}
func (f *fakeService) SyncRegistry(context.Context, app.ProgressSink) error {
	return errors.New("unused")
}
func (f *fakeService) ValidateRegistry(context.Context, string) error {
	return errors.New("unused")
}
func (f *fakeService) CheckTarLinkVersion(context.Context) (app.TarLinkVersion, error) {
	return f.tarlinkVersion, nil
}
func (f *fakeService) CheckTarLinkVersionFresh(context.Context) (app.TarLinkVersion, error) {
	return f.tarlinkVersion, nil
}
func (f *fakeService) UpgradeTarLink(context.Context, app.ProgressSink) (app.TarLinkVersion, error) {
	return app.TarLinkVersion{}, errors.New("unused")
}
func (f *fakeService) CheckInstallPath(string) ([]app.PathConflict, error) {
	return f.pathConflicts, nil
}
func (f *fakeService) Doctor(context.Context) (app.DoctorReport, error) {
	return app.DoctorReport{}, nil
}

func TestModelLoadsAndShowsApplications(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", RegistryVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service}
	message := m.Init()()
	var updated tea.Model = m
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, command := range batch {
			updated, _ = updated.Update(command())
		}
	} else {
		updated, _ = updated.Update(message)
	}
	view := updated.(model).View()
	if !strings.Contains(view.Content, "Blender") || !strings.Contains(view.Content, "APPLICATIONS") || !view.AltScreen {
		t.Fatalf("unexpected view: %q", view.Content)
	}
}

func TestPresentationUsesPanelAndSelectableControlLabels(t *testing.T) {
	m := model{screen: screenAvailable, width: 80, available: []app.Application{{ID: "one", Name: "One"}}}
	view := m.View().Content
	if !strings.Contains(view, "TarLink") || !strings.Contains(view, "[ All ]") {
		t.Fatalf("presentation omitted panel/control labels: %q", view)
	}
}

func TestGameDataRequirementIsShown(t *testing.T) {
	value := app.Application{ID: "banjo", Name: "Banjo", Requirements: []string{"original-game-data"}}
	m := model{screen: screenDetails, detail: &value, width: 80}
	if !strings.Contains(m.View().Content, "Requires: Original game data") {
		t.Fatalf("details view = %q", m.View().Content)
	}
	if !strings.Contains(installedLabel(value), "Game data required") {
		t.Fatal("list label omitted game-data marker")
	}
}

func TestTarLinkUpgradeNotificationAndBinding(t *testing.T) {
	service := &fakeService{tarlinkVersion: app.TarLinkVersion{Current: "0.4.2", Latest: "0.5.0", UpgradeAvailable: true}}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable}
	updated, _ := m.Update(m.checkVersionCmd()())
	modelAfterCheck := updated.(model)
	if !modelAfterCheck.upgradeAvailable || !strings.Contains(modelAfterCheck.View().Content, "press U") {
		t.Fatal("upgrade notice was not rendered")
	}
	updated, command := modelAfterCheck.Update(tea.KeyPressMsg{Text: "U"})
	if command != nil || updated.(model).screen != screenUpgrade {
		t.Fatal("U did not open upgrade confirmation")
	}
	cancelled, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || cancelled.(model).screen != screenAvailable {
		t.Fatal("upgrade cancellation did not return to the prior screen")
	}
}

func TestMouseModeAndRenderedListHitTesting(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "One"},
		{ID: "two", Name: "Two"},
		{ID: "three", Name: "Three"},
	}
	m := model{
		screen:           screenAvailable,
		available:        values,
		width:            80,
		height:           12,
		query:            "search",
		upgradeAvailable: true,
		tarlinkVersion:   app.TarLinkVersion{Current: "0.6.1", Latest: "0.6.2"},
	}
	view := m.View()
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode = %v, want cell motion", view.MouseMode)
	}
	if !strings.Contains(view.Content, "UPDATE AVAILABLE 0.6.1 -> 0.6.2 [U]") {
		t.Fatalf("update banner missing from view: %q", view.Content)
	}
	start, _ := m.listBounds()
	updated, command := m.Update(tea.MouseClickMsg{X: 1, Y: start + 1, Button: tea.MouseLeft})
	if command != nil || updated.(model).selected != 1 {
		t.Fatalf("visible row click selected=%d command=%v", updated.(model).selected, command)
	}
	updated, command = updated.(model).Update(tea.MouseClickMsg{X: 1, Y: m.height - 1, Button: tea.MouseLeft})
	if command != nil || updated.(model).selected != 1 {
		t.Fatalf("footer click changed selection: selected=%d command=%v", updated.(model).selected, command)
	}
}

func TestMouseWheelMovesThreeRowsWithBoundaries(t *testing.T) {
	values := make([]app.Application, 10)
	for i := range values {
		values[i] = app.Application{ID: string(rune('a' + i)), Name: "Application"}
	}
	m := model{screen: screenAvailable, available: values, width: 80, height: 12}
	update := func(button tea.MouseButton) {
		updated, command := m.Update(tea.MouseWheelMsg{Button: button})
		if command != nil {
			t.Fatalf("wheel command = %v", command)
		}
		m = updated.(model)
	}
	update(tea.MouseWheelUp)
	if m.selected != 0 {
		t.Fatalf("wheel up crossed upper boundary: %d", m.selected)
	}
	update(tea.MouseWheelDown)
	update(tea.MouseWheelDown)
	update(tea.MouseWheelDown)
	update(tea.MouseWheelDown)
	if m.selected != len(values)-1 {
		t.Fatalf("wheel down crossed lower boundary: %d", m.selected)
	}
}

func TestBusyFooterDoesNotExposeNavigationControls(t *testing.T) {
	m := model{screen: screenAvailable, busy: "Installing", width: 80, height: 12}
	if got := m.footer(); got != "Esc Cancel  q Quit" {
		t.Fatalf("busy footer = %q", got)
	}
}

func TestHelpUsesContextRelevantBindings(t *testing.T) {
	list := model{screen: screenAvailable, width: 120}
	help := list.helpView()
	for _, value := range []string{"Navigate", "Filter", "Details", "Search", "Installed", "Updates", "Quit"} {
		if !strings.Contains(help, value) {
			t.Fatalf("list help omitted %q: %q", value, help)
		}
	}
	upgradeHelp := (model{screen: screenAvailable, width: 120, upgradeAvailable: true}).helpView()
	if !strings.Contains(upgradeHelp, "Filter") {
		t.Fatalf("upgrade help omitted filter navigation: %q", upgradeHelp)
	}
	installedUpgradeHelp := (model{screen: screenInstalled, width: 120, upgradeAvailable: true}).helpView()
	if strings.Contains(installedUpgradeHelp, "Filter") {
		t.Fatalf("installed help exposed unavailable filter navigation: %q", installedUpgradeHelp)
	}
	installedHelp := (model{screen: screenInstalled, width: 120}).helpView()
	if strings.Contains(installedHelp, "Filter") {
		t.Fatalf("installed help exposed unavailable filter navigation: %q", installedHelp)
	}
	details := model{screen: screenDetails, width: 120}
	help = details.helpView()
	if !strings.Contains(help, "Back") || strings.Contains(help, "Search") || strings.Contains(help, "Updates") {
		t.Fatalf("details help exposed irrelevant actions: %q", help)
	}
}

func TestApplicationFilterCyclesAndResetsViewport(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "One", InstalledVersion: "1.0"},
		{ID: "two", Name: "Two"},
		{ID: "three", Name: "Three", InstalledVersion: "1.0"},
	}
	m := model{screen: screenAvailable, available: values, selected: 2, listOffset: 2, width: 80, height: 12}

	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if command != nil {
		t.Fatal("filter change returned a command")
	}
	m = updated.(model)
	if m.applicationFilter != filterNotInstalled || m.selected != 0 || m.listOffset != 0 || len(m.visibleApplications()) != 1 || m.visibleApplications()[0].ID != "two" {
		t.Fatalf("not-installed filter state = %#v", m)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if m.applicationFilter != filterAll || len(m.visibleApplications()) != len(values) {
		t.Fatalf("filter did not cycle back to all: %#v", m)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if updated.(model).applicationFilter != filterInstalled {
		t.Fatalf("right did not select installed filter: %#v", updated)
	}
}

func TestApplicationFilterEmptyResultsAndUpdatesPreservation(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One", InstalledVersion: "1.0", UpdateAvailable: true}}
	m := model{screen: screenAvailable, available: values, installed: values, width: 80, height: 12}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	m = updated.(model)
	if len(m.visibleApplications()) != 0 || m.selected != 0 || !strings.Contains(m.View().Content, "No applications.") {
		t.Fatalf("empty not-installed filter = %#v, view=%q", m, m.View().Content)
	}
	updated, command := m.Update(tea.KeyPressMsg{Text: "u"})
	if command != nil || updated.(model).screen != screenUpdates || len(updated.(model).visibleApplications()) != 1 {
		t.Fatalf("updates binding changed: model=%#v command=%v", updated, command)
	}
}

func TestApplicationFilterSearchAndDetailsInteraction(t *testing.T) {
	values := []app.Application{
		{ID: "installed", Name: "Installed", InstalledVersion: "1.0"},
		{ID: "new", Name: "New"},
	}
	service := &fakeService{applications: values}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: values, applicationFilter: filterInstalled, query: "installed"}
	cmd, _ := m.searchCmd()
	updated, _ := m.Update(cmd())
	m = updated.(model)
	if len(m.visibleApplications()) != 1 || m.visibleApplications()[0].ID != "installed" || m.selected != 0 || m.listOffset != 0 {
		t.Fatalf("search ignored active filter: %#v", m)
	}
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || updated.(model).screen != screenDetails || updated.(model).detail == nil || updated.(model).detail.ID != "installed" {
		t.Fatalf("filtered detail interaction failed: model=%#v command=%v", updated, command)
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || updated.(model).screen != screenAvailable || updated.(model).applicationFilter != filterInstalled {
		t.Fatalf("details back lost filter: model=%#v", updated)
	}
}

func TestSelectedRowIsVisuallyHighlighted(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "One"},
		{ID: "two", Name: "Two"},
		{ID: "three", Name: "Three"},
	}
	m := model{
		screen: screenInstalled, installed: values, selected: 1,
		width: 80, height: 12, color: true, theme: newTheme(true),
	}
	lines := strings.Split(m.View().Content, "\n")
	var selectedLine string
	for _, line := range lines {
		if strings.Contains(line, "Two") {
			selectedLine = line
		}
	}
	if selectedLine == "" {
		t.Fatalf("selected row not rendered: %q", m.View().Content)
	}
	if !strings.Contains(selectedLine, "\x1b[") {
		t.Fatalf("selected row lacks visual highlight: %q", selectedLine)
	}
	// The non-selected row must not be styled.
	for _, line := range lines {
		if strings.Contains(line, "One") && strings.Contains(line, "\x1b[") {
			t.Fatalf("non-selected row was styled: %q", line)
		}
	}
}

func TestNoColorThemeAndProgressRemainPlainText(t *testing.T) {
	m := model{
		color:    false,
		theme:    newTheme(false),
		busy:     "Installing",
		progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 50, BytesTotal: 100},
		width:    80,
	}
	view := m.View().Content
	if strings.Contains(view, "\x1b[") {
		t.Fatalf("NO_COLOR rendering contains ANSI styling: %q", view)
	}
	if !strings.Contains(view, "Downloading") || !strings.Contains(view, "50%") {
		t.Fatalf("plain progress is not understandable: %q", view)
	}
}

func TestRollbackDelegatesToService(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	updated, command := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if command != nil || updated.(model).screen != screenRollback {
		t.Fatal("rollback confirmation did not open")
	}
	updated, command = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || updated.(model).busy == "" {
		t.Fatal("confirmed rollback did not start")
	}
	result := command()
	if result.(operationMsg).err != nil || service.rolledBack != "blender" {
		t.Fatalf("rollback result=%#v id=%q", result, service.rolledBack)
	}
}

func TestUninstallRequiresConfirmation(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}

	updated, command := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	modelAfterKey := updated.(model)
	if command != nil || modelAfterKey.screen != screenUninstall {
		t.Fatalf("uninstall confirmation did not open: screen=%v command=%v", modelAfterKey.screen, command)
	}
	view := modelAfterKey.View().Content
	if !strings.Contains(view, "UNINSTALL") || !strings.Contains(view, "Enter Confirm") || !strings.Contains(view, "Blender") {
		t.Fatalf("confirmation view is unclear: %q", view)
	}
	if service.uninstalled != "" {
		t.Fatalf("opening confirmation called uninstall for %q", service.uninstalled)
	}
}

func TestInstallPathConflictRequiresConfirmation(t *testing.T) {
	service := &fakeService{
		applications: []app.Application{{ID: "blender", Name: "Blender"}},
		pathConflicts: []app.PathConflict{
			{Type: "shadowed", Executable: "blender", Directory: "/usr/bin", Candidate: "/usr/bin/blender"},
		},
	}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("install path check did not start")
	}
	updated, command = updated.(model).Update(command())
	modelAfterCheck := updated.(model)
	if command != nil || modelAfterCheck.screen != screenInstallConfirm {
		t.Fatalf("path conflict did not open confirmation: screen=%v command=%v", modelAfterCheck.screen, command)
	}
	view := modelAfterCheck.View().Content
	if !strings.Contains(view, "PATH CONFLICT") || !strings.Contains(view, "shadow") || !strings.Contains(view, "Enter Install anyway") {
		t.Fatalf("path conflict view is unclear: %q", view)
	}
	// Cancelling returns to details without installing.
	updated, command = modelAfterCheck.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterCancel := updated.(model)
	if command != nil || modelAfterCancel.screen != screenDetails || modelAfterCancel.detail == nil {
		t.Fatalf("cancel did not return to details: model=%#v command=%v", modelAfterCancel, command)
	}
	// Confirming installs despite the conflict.
	updated, command = modelAfterCancel.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("re-entry path check did not start")
	}
	updated, command = updated.(model).Update(command())
	modelAfterConfirm := updated.(model)
	if command != nil || modelAfterConfirm.screen != screenInstallConfirm {
		t.Fatalf("re-entry did not reach install confirmation: model=%#v", modelAfterConfirm)
	}
	updated, command = modelAfterConfirm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || updated.(model).busy != "Installing blender" {
		t.Fatalf("confirmed install did not start: model=%#v command=%v", updated.(model), command)
	}
}

func TestUninstallCancellationDoesNotCallService(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterCancel := updated.(model)
	if command != nil || modelAfterCancel.screen != screenInstalled || modelAfterCancel.detail != nil {
		t.Fatalf("cancel did not return to installed screen: model=%#v command=%v", modelAfterCancel, command)
	}
	if service.uninstalled != "" {
		t.Fatalf("cancel called uninstall for %q", service.uninstalled)
	}
}

func TestUninstallDelegatesAndRefreshesState(t *testing.T) {
	available := []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}
	service := &fakeService{applications: available, installedValues: available}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: available}

	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	modelAfterConfirm := updated.(model)
	if command == nil || modelAfterConfirm.busy != "Uninstalling" {
		t.Fatal("confirmed uninstall did not start")
	}
	operation := command()
	if operation.(operationMsg).err != nil || service.uninstalled != "blender" {
		t.Fatalf("uninstall result=%#v id=%q", operation, service.uninstalled)
	}

	updated, command = modelAfterConfirm.Update(operation)
	modelAfterOperation := updated.(model)
	if command == nil || modelAfterOperation.screen != screenInstalled || modelAfterOperation.status != "Uninstalled blender" {
		t.Fatalf("successful uninstall did not return to installed state: model=%#v", modelAfterOperation)
	}
	updated, _ = modelAfterOperation.Update(command())
	modelAfterRefresh := updated.(model)
	if len(modelAfterRefresh.installed) != 0 {
		t.Fatalf("installed state was not refreshed: %#v", modelAfterRefresh.installed)
	}
}

func TestUninstallFromDetailsRefreshesStateAndBackNavigation(t *testing.T) {
	available := []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}
	service := &fakeService{applications: available, installedValues: available}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: available}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	modelAfterConfirm := updated.(model)
	if command == nil || modelAfterConfirm.busy != "Uninstalling" {
		t.Fatal("details uninstall did not start")
	}

	operation := command()
	updated, command = modelAfterConfirm.Update(operation)
	modelAfterOperation := updated.(model)
	if command == nil || modelAfterOperation.screen != screenDetails {
		t.Fatalf("successful details uninstall left unexpected screen: model=%#v", modelAfterOperation)
	}

	updated, _ = modelAfterOperation.Update(command())
	modelAfterRefresh := updated.(model)
	if modelAfterRefresh.detail == nil || modelAfterRefresh.detail.InstalledVersion != "" {
		t.Fatalf("details state was not refreshed after uninstall: %#v", modelAfterRefresh.detail)
	}
	if len(modelAfterRefresh.installed) != 0 {
		t.Fatalf("installed state was not refreshed after uninstall: %#v", modelAfterRefresh.installed)
	}

	updated, command = modelAfterRefresh.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterBack := updated.(model)
	if command != nil || modelAfterBack.screen != screenInstalled || modelAfterBack.detail != nil {
		t.Fatalf("details uninstall back-navigation failed: model=%#v command=%v", modelAfterBack, command)
	}
}

func TestUninstallFromVersionsRefreshesDetailsAndBackNavigation(t *testing.T) {
	available := []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}
	service := &fakeService{applications: available, installedValues: available}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: available}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	if command == nil {
		t.Fatal("versions request did not start from details")
	}
	updated, _ = updated.(model).Update(command())
	modelAfterVersions := updated.(model)
	if modelAfterVersions.screen != screenVersions {
		t.Fatalf("versions request did not open versions screen: model=%#v", modelAfterVersions)
	}

	updated, command = modelAfterVersions.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	modelAfterConfirm := updated.(model)
	if command != nil || modelAfterConfirm.screen != screenUninstall {
		t.Fatalf("versions uninstall confirmation did not open: model=%#v command=%v", modelAfterConfirm, command)
	}
	updated, command = modelAfterConfirm.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil || updated.(model).busy != "Uninstalling" {
		t.Fatal("confirmed versions uninstall did not start")
	}

	operation := command()
	updated, command = updated.(model).Update(operation)
	modelAfterOperation := updated.(model)
	if command == nil || modelAfterOperation.screen != screenDetails {
		t.Fatalf("successful versions uninstall did not return to details: model=%#v", modelAfterOperation)
	}
	updated, _ = modelAfterOperation.Update(command())
	modelAfterRefresh := updated.(model)
	if modelAfterRefresh.detail == nil || modelAfterRefresh.detail.InstalledVersion != "" {
		t.Fatalf("details state was not refreshed after versions uninstall: %#v", modelAfterRefresh.detail)
	}
	if len(modelAfterRefresh.installed) != 0 {
		t.Fatalf("installed state was not refreshed after versions uninstall: %#v", modelAfterRefresh.installed)
	}

	updated, command = modelAfterRefresh.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterBack := updated.(model)
	if command != nil || modelAfterBack.screen != screenInstalled || modelAfterBack.detail != nil {
		t.Fatalf("versions uninstall back-navigation failed: model=%#v command=%v", modelAfterBack, command)
	}
}

func TestUninstallReportsErrorsWithoutLeavingConfirmation(t *testing.T) {
	service := &fakeService{
		applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}},
		uninstallErr: errors.New("permission denied"),
	}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	operation := command()
	updated, command = updated.(model).Update(operation)
	modelAfterError := updated.(model)
	if command != nil || modelAfterError.screen != screenUninstall || !strings.Contains(modelAfterError.View().Content, "permission denied") {
		t.Fatalf("uninstall error was not shown on confirmation screen: model=%#v command=%v", modelAfterError, command)
	}
}

func TestUninstallEnterOnOtherScreensDoesNotCallService(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || updated.(model).screen != screenDetails || service.uninstalled != "" {
		t.Fatalf("enter unexpectedly confirmed uninstall: model=%#v command=%v", updated, command)
	}
}

func TestUninstallFromDetailsCancelThenBackReturnsToList(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterBack := updated.(model)
	if command != nil || modelAfterBack.screen != screenInstalled {
		t.Fatalf("details uninstall cancel did not return to list: model=%#v command=%v", modelAfterBack, command)
	}
}

func TestRollbackFromDetailsCancelThenBackReturnsToList(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	updated, _ = updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterBack := updated.(model)
	if command != nil || modelAfterBack.screen != screenInstalled {
		t.Fatalf("details rollback cancel did not return to list: model=%#v command=%v", modelAfterBack, command)
	}
}

func TestVersionsFromDetailsEscapesBackThroughDetailsToList(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}

	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	if command == nil || updated.(model).screen != screenDetails {
		t.Fatalf("versions request did not start from details: model=%#v command=%v", updated, command)
	}
	updated, _ = updated.(model).Update(command())
	modelAfterVersions := updated.(model)
	if modelAfterVersions.screen != screenVersions {
		t.Fatalf("versions request did not open versions screen: model=%#v", modelAfterVersions)
	}

	updated, command = modelAfterVersions.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterDetails := updated.(model)
	if command != nil || modelAfterDetails.screen != screenDetails {
		t.Fatalf("first Esc did not return to details: model=%#v command=%v", modelAfterDetails, command)
	}
	updated, command = modelAfterDetails.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterList := updated.(model)
	if command != nil || modelAfterList.screen != screenInstalled || modelAfterList.detail != nil {
		t.Fatalf("second Esc did not return to installed list: model=%#v command=%v", modelAfterList, command)
	}
}

func TestVersionsFromInstalledListEscapesToList(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}

	updated, command := m.Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	if command == nil || updated.(model).screen != screenInstalled {
		t.Fatalf("versions request did not start from installed list: model=%#v command=%v", updated, command)
	}
	updated, _ = updated.(model).Update(command())
	modelAfterVersions := updated.(model)
	if modelAfterVersions.screen != screenVersions {
		t.Fatalf("versions request did not open versions screen: model=%#v", modelAfterVersions)
	}

	updated, command = modelAfterVersions.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	modelAfterBack := updated.(model)
	if command != nil || modelAfterBack.screen != screenInstalled || modelAfterBack.detail != nil {
		t.Fatalf("versions list back-navigation failed: model=%#v command=%v", modelAfterBack, command)
	}
}

func TestQuitKeysAndRootEscape(t *testing.T) {
	m := model{screen: screenAvailable}
	for _, key := range []string{"q", "ctrl+c"} {
		updated, command := m.Update(tea.KeyPressMsg{Text: key})
		if command == nil {
			t.Fatalf("%s did not quit: %#v", key, updated)
		}
	}
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if command != nil || updated.(model).screen != screenAvailable {
		t.Fatalf("root escape changed state: %#v", updated)
	}
}

func TestResizeAndNarrowRenderingStayWithinWidth(t *testing.T) {
	value := app.Application{ID: "cafe", Name: "日本語 Application", RegistryVersion: "5.2.0", InstalledVersion: "5.1.0", UpdateAvailable: true}
	m := model{screen: screenInstalled, installed: []app.Application{value}, width: 24}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 10})
	m = updated.(model)
	if m.width != 24 || m.height != 10 {
		t.Fatalf("resize not stored: %#v", m)
	}
	for _, line := range strings.Split(m.View().Content, "\n") {
		if displayWidth(line) > m.width {
			t.Fatalf("line exceeds width: %d > %d: %q", displayWidth(line), m.width, line)
		}
	}
}

func TestApplicationStatusUsesUserTerminology(t *testing.T) {
	for _, tc := range []struct {
		value app.Application
		want  string
	}{
		{app.Application{}, "Not installed"},
		{app.Application{InstalledVersion: "1.0"}, "Installed"},
		{app.Application{InstalledVersion: "1.0", UpdateAvailable: true}, "Update available"},
		{app.Application{InstalledVersion: "1.0", UpdateAvailable: true, Pinned: true}, "Pinned"},
	} {
		if got := applicationStatus(tc.value); got != tc.want {
			t.Errorf("status = %q, want %q", got, tc.want)
		}
	}
	if strings.Contains(installedLabel(app.Application{}), "Install") || strings.Contains(installedLabel(app.Application{InstalledVersion: "1.0"}), "current") {
		t.Fatal("action-oriented state label regressed")
	}
}

func TestInstalledLabelShowsTrackingChannelAndPin(t *testing.T) {
	value := app.Application{InstalledVersion: "2.7.513", InstalledChannel: "nightly", Pinned: true}
	label := installedLabel(value)
	if !strings.Contains(label, "nightly") || !strings.Contains(label, "Pinned") {
		t.Fatalf("installed label = %q, want channel and pin state", label)
	}
}

func TestUpdatesOmitsPinnedApplications(t *testing.T) {
	values := []app.Application{
		{ID: "pinned", InstalledVersion: "1.0", UpdateAvailable: true, Pinned: true},
		{ID: "tracked", InstalledVersion: "1.0", UpdateAvailable: true},
	}
	got := updates(values)
	if len(got) != 1 || got[0].ID != "tracked" {
		t.Fatalf("updates() = %#v, want only unpinned update", got)
	}
}

func TestPinnedApplicationDoesNotStartTUIUpdate(t *testing.T) {
	value := app.Application{ID: "pcsx2", Name: "PCSX2", InstalledVersion: "2.7.513", UpdateAvailable: true, Pinned: true}
	m := model{screen: screenDetails, detail: &value, width: 100, height: 20}
	updated, command := m.activateSelected()
	if command != nil {
		t.Fatal("pinned application started an update command")
	}
	if !strings.Contains(updated.(model).status, "pinned") {
		t.Fatalf("status = %q, want pinned message", updated.(model).status)
	}
}

func TestViewportKeepsSelectionVisible(t *testing.T) {
	values := make([]app.Application, 8)
	for i := range values {
		values[i] = app.Application{ID: string(rune('a' + i)), Name: "Application"}
	}
	m := model{screen: screenAvailable, available: values, width: 80, height: 16}
	for i := 0; i < len(values)-1; i++ {
		m.selected = i
		m.clampViewport()
	}
	if m.selected < m.listOffset || m.selected >= m.listOffset+m.listRows() {
		t.Fatalf("selected=%d offset=%d rows=%d", m.selected, m.listOffset, m.listRows())
	}
	m.selected = 0
	m.clampViewport()
	if m.listOffset != 0 {
		t.Fatalf("viewport did not scroll back: %d", m.listOffset)
	}
}

func TestSpeedEstimatorIsSmoothedAndDeterministic(t *testing.T) {
	var estimator speedEstimator
	start := time.Unix(0, 0)
	if got := estimator.Add(start, 0); got != 0 {
		t.Fatalf("initial speed = %v", got)
	}
	if got := estimator.Add(start.Add(time.Second), 10<<20); got != 10<<20 {
		t.Fatalf("speed = %v", got)
	}
	if got := formatRate(10 << 20); got != "10 MiB/s" {
		t.Fatalf("rate = %q", got)
	}
	if got := formatDuration(130); got != "2m 10s" {
		t.Fatalf("duration = %q", got)
	}
	if got := estimator.Add(start.Add(2*time.Second), 5<<20); got != 0 {
		t.Fatalf("negative delta speed = %v", got)
	}
	if estimator.Ready() {
		t.Fatal("reset estimator reported ETA readiness")
	}
}

func TestSpeedEstimatorUsesBoundedWindowAndETAWarmup(t *testing.T) {
	var estimator speedEstimator
	start := time.Unix(0, 0)
	if got := estimator.Add(start, 0); got != 0 || estimator.Ready() {
		t.Fatalf("initial sample = speed %v, ready %v", got, estimator.Ready())
	}
	if got := estimator.Add(start.Add(2*time.Second), 2<<20); got != 1<<20 || !estimator.Ready() {
		t.Fatalf("warmed sample = speed %v, ready %v", got, estimator.Ready())
	}
	if got := estimator.Add(start.Add(10*time.Second), 10<<20); got != 1<<20 {
		t.Fatalf("bounded-window speed = %v", got)
	}
	if got := estimator.Add(start.Add(9*time.Second), 11<<20); got != 0 || estimator.Ready() {
		t.Fatalf("backward sample = speed %v, ready %v", got, estimator.Ready())
	}
}

func TestProgressFormattingRejectsInvalidValues(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if got := formatRate(rate); got != "" {
			t.Errorf("formatRate(%v) = %q", rate, got)
		}
	}
	for _, test := range []struct {
		rate float64
		want string
	}{
		{512, "512 B/s"},
		{1536, "1.5 KiB/s"},
		{12 << 10, "12 KiB/s"},
	} {
		if got := formatRate(test.rate); got != test.want {
			t.Errorf("formatRate(%v) = %q, want %q", test.rate, got, test.want)
		}
	}
	for _, seconds := range []float64{-1, math.NaN(), math.Inf(1)} {
		if got := formatDuration(seconds); got != "" {
			t.Errorf("formatDuration(%v) = %q", seconds, got)
		}
	}
}

func TestProgressCounterResetClearsDisplayedRate(t *testing.T) {
	m := model{progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 2}, progressSpeed: 10}
	updated, _ := m.Update(progressMsg{event: app.Progress{Stage: app.ProgressDownloading, BytesDone: 0}})
	if updated.(model).progressSpeed != 0 {
		t.Fatalf("displayed speed after reset = %v", updated.(model).progressSpeed)
	}
}

func TestOperationHubCoalescesRoutineProgressAndKeepsResult(t *testing.T) {
	hub := &operationHub{wake: make(chan struct{}, 1), result: make(chan operationMsg, 1)}
	for i := int64(0); i < 1000; i++ {
		hub.publish(app.Progress{Stage: app.ProgressDownloading, BytesDone: i, BytesTotal: 1000})
	}
	first := hub.next(context.Background()).(progressMsg)
	if first.event.Stage != app.ProgressDownloading {
		t.Fatalf("first event = %#v", first.event)
	}
	hub.lastEmit = time.Time{}
	second := hub.next(context.Background()).(progressMsg)
	if second.event.BytesDone != 999 {
		t.Fatalf("latest progress = %#v", second.event)
	}
	hub.finish(operationMsg{message: "complete"})
	if result := hub.next(context.Background()).(operationMsg); result.message != "complete" {
		t.Fatalf("result = %#v", result)
	}
}

func TestOperationHubEmitsLatestProgressBeforeCompletion(t *testing.T) {
	hub := &operationHub{
		wake: make(chan struct{}, 1), result: make(chan operationMsg, 1),
		lastEmit: time.Now(), latest: &app.Progress{Stage: app.ProgressExtracting, BytesDone: 20},
	}
	hub.result <- operationMsg{message: "complete"}
	message := hub.next(context.Background())
	progress, ok := message.(progressMsg)
	if !ok || progress.event.BytesDone != 20 {
		t.Fatalf("first message = %#v, want latest progress", message)
	}
	if message = hub.next(context.Background()); message.(operationMsg).message != "complete" {
		t.Fatalf("completion message = %#v", message)
	}
}

func TestProgressEventsRenderLifecycleAndUnknownLength(t *testing.T) {
	service := &fakeService{
		applications: []app.Application{{ID: "blender", Name: "Blender"}},
		installProgress: []app.Progress{
			{Stage: app.ProgressDownloading, BytesDone: 245 << 20, BytesTotal: 365 << 20},
			{Stage: app.ProgressVerifying, BytesDone: 12, BytesTotal: 0},
		},
	}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("install path check did not start")
	}
	// No PATH conflicts: the path check proceeds directly to install.
	updated, command = updated.(model).Update(command())
	if command == nil {
		t.Fatal("install did not start after path check")
	}
	updated, command = updated.(model).Update(command())
	progressModel := updated.(model)
	if !strings.Contains(progressModel.View().Content, "Downloading") || !strings.Contains(progressModel.View().Content, "67%") {
		t.Fatalf("download progress not rendered: %q", progressModel.View().Content)
	}
	updated, command = progressModel.Update(command())
	progressModel = updated.(model)
	if command == nil || !strings.Contains(progressModel.View().Content, "Verifying") || strings.Contains(progressModel.View().Content, "%") {
		t.Fatalf("unknown length progress rendered a percentage: %q", updated.(model).View().Content)
	}
	operationMessage := command()
	completed, refresh := progressModel.Update(operationMessage)
	if refresh == nil || completed.(model).busy != "" || completed.(model).progress.Stage != "" {
		t.Fatalf("operation did not clean progress state: %#v", completed)
	}
}

func displayWidth(value string) int {
	return displaywidth.Options{ControlSequences: true}.String(value)
}

func TestProgressBarFixedWidthAtRepresentativeSizes(t *testing.T) {
	bar := newProgress(false)
	if bar.Width() != progressBarWidth {
		t.Fatalf("initial progress bar width = %d, want %d", bar.Width(), progressBarWidth)
	}
	if got := displayWidth(bar.ViewAs(0.5)); got != progressBarWidth {
		t.Fatalf("rendered progress bar width = %d, want %d", got, progressBarWidth)
	}
	// Stable standard width across wide and normal resize, shrinking only on
	// genuinely narrow terminals, with graceful rendering at every size.
	for _, tc := range []struct {
		terminal int
		want     int
	}{
		{120, progressBarWidth},
		{80, progressBarWidth},
		{60, progressBarWidth},
		{40, 18},
		{24, 14},
		{20, 14},
	} {
		m := model{
			color: false, theme: newTheme(false), busy: "Installing",
			progress:    app.Progress{Stage: app.ProgressDownloading, BytesDone: 50, BytesTotal: 100},
			width:       tc.terminal,
			height:      12,
			progressBar: newProgress(false),
		}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: tc.terminal, Height: 12})
		m = updated.(model)
		if m.progressBar.Width() != tc.want {
			t.Fatalf("width %d: progress bar width = %d, want %d", tc.terminal, m.progressBar.Width(), tc.want)
		}
		line := m.progressLine()
		if !strings.Contains(line, "50%") {
			t.Fatalf("width %d: progress line missing percentage: %q", tc.terminal, line)
		}
	}
	// Wide/normal resize keeps the standard width stable.
	stable := model{progressBar: newProgress(false), width: 80}
	updated, _ := stable.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	stable = updated.(model)
	updated, _ = stable.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	stable = updated.(model)
	if stable.progressBar.Width() != progressBarWidth {
		t.Fatalf("wide/normal resize changed bar width to %d, want stable %d", stable.progressBar.Width(), progressBarWidth)
	}
}

func TestProgressBarWidthHelperIsDeterministic(t *testing.T) {
	for _, tc := range []struct {
		terminal int
		want     int
	}{
		{200, 24}, {120, 24}, {80, 24}, {60, 24},
		{59, 18}, {40, 18}, {39, 14}, {24, 14}, {10, 14}, {0, 14},
	} {
		if got := progressBarWidthFor(tc.terminal); got != tc.want {
			t.Errorf("progressBarWidthFor(%d) = %d, want %d", tc.terminal, got, tc.want)
		}
	}
}

func TestBusyHelpShowsEscCancelAndQuit(t *testing.T) {
	m := model{screen: screenAvailable, busy: "Installing", width: 120, height: 12}
	help := m.helpView()
	if !strings.Contains(help, "Esc Cancel") || !strings.Contains(help, "q Quit") {
		t.Fatalf("busy help must show Esc Cancel and q Quit: %q", help)
	}
	if strings.Contains(help, "Back") {
		t.Fatalf("busy help mislabeled Esc as Back: %q", help)
	}
}

func TestProgressBarRenderingFitsTerminalWidth(t *testing.T) {
	for _, width := range []int{120, 80, 40, 24, 20} {
		m := model{
			color: false, theme: newTheme(false), busy: "Installing",
			progress:      app.Progress{Stage: app.ProgressDownloading, BytesDone: 245 << 20, BytesTotal: 365 << 20},
			progressSpeed: 1 << 20,
			width:         width,
			height:        16,
			progressBar:   newProgress(false),
		}
		updated, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 16})
		for _, line := range strings.Split(updated.(model).View().Content, "\n") {
			if displayWidth(line) > width {
				t.Fatalf("width %d: line exceeds width: %q", width, line)
			}
		}
	}
}

func TestOperationCancellationViaContextAndReuse(t *testing.T) {
	service := &fakeService{
		applications:    []app.Application{{ID: "blender", Name: "Blender"}},
		blockOnCancel:   true,
		installStarted:  make(chan struct{}),
		installCanceled: make(chan struct{}),
	}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("install path check did not start")
	}
	// No PATH conflicts: path check proceeds to the cancellable install.
	updated, command = updated.(model).Update(command())
	modelBusy := updated.(model)
	if command == nil || modelBusy.busy == "" || modelBusy.opCancel == nil {
		t.Fatalf("install did not start as cancellable operation: %#v", modelBusy)
	}
	var result tea.Msg
	done := make(chan struct{})
	go func() {
		result = command()
		close(done)
	}()
	select {
	case <-service.installStarted:
	case <-time.After(time.Second):
		t.Fatal("operation did not reach the service")
	}
	// Esc cancels the active operation.
	cancelled, _ := modelBusy.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	select {
	case <-service.installCanceled:
	case <-time.After(time.Second):
		t.Fatal("active operation was not cancelled via context")
	}
	<-done
	message, ok := result.(operationMsg)
	if !ok || !errors.Is(message.err, context.Canceled) {
		t.Fatalf("operation result = %#v, want context.Canceled", result)
	}
	finished, _ := cancelled.(model).Update(result)
	modelDone := finished.(model)
	if modelDone.busy != "" || modelDone.err != nil || modelDone.opCancel != nil || modelDone.progress.Stage != "" {
		t.Fatalf("cancellation left stale state: %#v", modelDone)
	}
	if !strings.Contains(modelDone.View().Content, "cancelled") {
		t.Fatalf("cancellation not indicated to the user: %q", modelDone.View().Content)
	}
	// A subsequent operation remains reusable on the same model.
	service.blockOnCancel = false
	updated, command = modelDone.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatalf("path check did not restart: %#v", updated.(model))
	}
	updated, command = updated.(model).Update(command())
	if command == nil || updated.(model).opCancel == nil {
		t.Fatalf("subsequent operation not reusable: %#v", updated.(model))
	}
	if message := command(); message.(operationMsg).err != nil {
		t.Fatalf("subsequent operation failed: %#v", message)
	}
}

func TestQuitDuringBusyCancelsOperation(t *testing.T) {
	service := &fakeService{
		applications:    []app.Application{{ID: "blender", Name: "Blender"}},
		blockOnCancel:   true,
		installStarted:  make(chan struct{}),
		installCanceled: make(chan struct{}),
	}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("install path check did not start")
	}
	updated, command = updated.(model).Update(command())
	modelBusy := updated.(model)
	if command == nil {
		t.Fatal("install did not start")
	}
	done := make(chan struct{})
	go func() {
		command()
		close(done)
	}()
	select {
	case <-service.installStarted:
	case <-time.After(time.Second):
		t.Fatal("operation did not reach the service")
	}
	quit, quitCmd := modelBusy.Update(tea.KeyPressMsg{Text: "q"})
	if quitCmd == nil {
		t.Fatal("q did not quit")
	}
	select {
	case <-service.installCanceled:
	case <-time.After(time.Second):
		t.Fatal("quit did not cancel the active operation")
	}
	<-done
	_ = quit
}

func TestStaleFeedbackClearedOnNavigationAndNewOperation(t *testing.T) {
	m := model{screen: screenAvailable, status: "Installed blender 5.2.0", err: errors.New("stale error"), width: 80, height: 12}
	updated, command := m.Update(tea.KeyPressMsg{Text: "i"})
	modelAfterNav := updated.(model)
	if command != nil || modelAfterNav.status != "" || modelAfterNav.err != nil {
		t.Fatalf("navigation did not clear stale feedback: %#v", modelAfterNav)
	}

	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}}}
	start := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications, err: errors.New("stale error")}
	updated, command = start.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	modelAfterConfirm := updated.(model)
	if command != nil || modelAfterConfirm.err != nil {
		t.Fatalf("opening an operation did not clear stale error: %#v", modelAfterConfirm)
	}
}

func TestStaleFeedbackClearedOnSelectionAndFilterNavigation(t *testing.T) {
	values := []app.Application{
		{ID: "one", Name: "One", InstalledVersion: "1.0"},
		{ID: "two", Name: "Two", InstalledVersion: "1.0"},
		{ID: "three", Name: "Three"},
	}
	// Success feedback must disappear on selection navigation (Up/Down).
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyUp},
		{Code: tea.KeyDown},
	} {
		m := model{
			screen: screenInstalled, installed: values, selected: 1,
			status: "Installed blender 5.2.0", err: errors.New("stale error"), width: 80, height: 12,
		}
		updated, command := m.Update(key)
		modelAfter := updated.(model)
		if command != nil || modelAfter.status != "" || modelAfter.err != nil {
			t.Fatalf("key %s did not clear stale feedback on selection: %#v", key, modelAfter)
		}
	}

	// Filter navigation (Left/Right) on the available screen must clear feedback.
	for _, key := range []tea.KeyPressMsg{
		{Code: tea.KeyLeft},
		{Code: tea.KeyRight},
	} {
		m := model{
			screen: screenAvailable, available: values, status: "Installed blender 5.2.0",
			err: errors.New("stale error"), width: 80, height: 12,
		}
		updated, command := m.Update(key)
		modelAfter := updated.(model)
		if command != nil || modelAfter.status != "" || modelAfter.err != nil {
			t.Fatalf("key %s did not clear stale feedback on filter change: %#v", key, modelAfter)
		}
	}

	// Starting a search must clear stale feedback.
	m := model{
		screen: screenInstalled, installed: values, status: "Installed blender 5.2.0",
		err: errors.New("stale error"), width: 80, height: 12,
	}
	updated, command := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	modelAfter := updated.(model)
	if command != nil || modelAfter.status != "" || modelAfter.err != nil {
		t.Fatalf("search did not clear stale feedback: %#v", modelAfter)
	}
}

func TestSelectionNavigationKeepsActiveBusyState(t *testing.T) {
	values := []app.Application{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	m := model{
		screen: screenInstalled, installed: values, busy: "Installing one",
		progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 10, BytesTotal: 100},
		width:    80, height: 12,
	}
	// Selection navigation while busy must not clear the active operation state.
	updated, command := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	modelAfter := updated.(model)
	if command != nil || modelAfter.busy != "Installing one" || modelAfter.progress.Stage != app.ProgressDownloading {
		t.Fatalf("busy operation state was disturbed by selection navigation: %#v", modelAfter)
	}
}

func TestNewOperationClearsStaleProgress(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender"}}}
	m := model{
		ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications,
		progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 10, BytesTotal: 100},
		width:    80,
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if command == nil {
		t.Fatal("install path check did not start")
	}
	updated, command = updated.(model).Update(command())
	modelBusy := updated.(model)
	if command == nil || modelBusy.progress.Stage != "" {
		t.Fatalf("starting operation did not clear stale progress: %#v", modelBusy)
	}
}

func TestResponsiveLayoutFitsAtRepresentativeSizes(t *testing.T) {
	detail := &app.Application{
		ID: "blender", Name: "Blender", Summary: "A 3D creation suite", Homepage: "https://blender.org",
		Categories: []string{"graphics"}, RegistryVersion: "5.2.0", InstalledVersion: "5.1.0", UpdateAvailable: true,
	}
	for _, width := range []int{120, 80, 40, 24, 20} {
		scenarios := []model{
			{screen: screenAvailable, available: []app.Application{{ID: "a", Name: "Alpha"}}, width: width, height: 16},
			{screen: screenInstalled, installed: []app.Application{{ID: "a", Name: "Alpha", InstalledVersion: "1.0"}}, width: width, height: 16},
			{screen: screenUpdates, installed: []app.Application{{ID: "a", Name: "Alpha", InstalledVersion: "1.0", UpdateAvailable: true}}, width: width, height: 16},
			{screen: screenDetails, detail: detail, width: width, height: 16},
			{screen: screenRollback, detail: detail, width: width, height: 16},
		}
		for _, m := range scenarios {
			view := m.View().Content
			if view == "" {
				t.Fatalf("width %d screen %d rendered empty view", width, m.screen)
			}
			for _, line := range strings.Split(view, "\n") {
				if displayWidth(line) > width {
					t.Fatalf("width %d screen %d: line exceeds terminal width: %q", width, m.screen, line)
				}
			}
		}
	}
}

func TestListBoundsMatchRenderedContent(t *testing.T) {
	values := []app.Application{{ID: "a", Name: "Alpha"}, {ID: "b", Name: "Beta"}}
	scenarios := []struct {
		name string
		m    model
	}{
		{"installed", model{screen: screenInstalled, installed: values, width: 80, height: 24}},
		{"available", model{screen: screenAvailable, available: values, width: 80, height: 24}},
		{"available-searching", model{screen: screenAvailable, available: values, searching: true, query: "app", width: 80, height: 24}},
		{"busy-progress", model{screen: screenInstalled, installed: values, busy: "Installing", progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 1, BytesTotal: 2}, width: 80, height: 24}},
		{"status", model{screen: screenInstalled, installed: values, status: "Installed alpha", width: 80, height: 24}},
		{"upgrade-banner", model{screen: screenInstalled, installed: values, upgradeAvailable: true, width: 80, height: 24}},
	}
	for _, sc := range scenarios {
		start, _ := sc.m.listBoundsWithoutRows()
		lines := strings.Split(strings.TrimSuffix(sc.m.View().Content, "\n"), "\n")
		if len(lines) <= start {
			t.Fatalf("%s: list start row %d but only %d lines rendered", sc.name, start, len(lines))
		}
		if !strings.Contains(lines[start], "Alpha") {
			t.Fatalf("%s: first application not at computed row %d: %q", sc.name, start, lines[start])
		}
		if sc.m.listRows() < 1 {
			t.Fatalf("%s: listRows %d < 1", sc.name, sc.m.listRows())
		}
	}
}

func TestFooterVisibleAtRepresentativeSizes(t *testing.T) {
	values := make([]app.Application, 20)
	for i := range values {
		values[i] = app.Application{ID: string(rune('a' + i)), Name: "Application " + string(rune('a'+i))}
	}
	detail := &app.Application{
		ID: "blender", Name: "Blender", Summary: "A 3D creation suite", Homepage: "https://blender.org",
		Categories: []string{"graphics"}, RegistryVersion: "5.2.0", InstalledVersion: "5.1.0", UpdateAvailable: true,
	}
	sizes := [][2]int{{120, 40}, {80, 24}, {60, 18}, {80, 12}, {60, 10}, {80, 6}}
	for _, size := range sizes {
		width, height := size[0], size[1]
		scenarios := []model{
			{screen: screenInstalled, installed: values, width: width, height: height},
			{screen: screenAvailable, available: values, width: width, height: height},
			{screen: screenAvailable, available: values, searching: true, query: "app", width: width, height: height},
			{screen: screenInstalled, installed: values, busy: "Installing", progress: app.Progress{Stage: app.ProgressDownloading, BytesDone: 1, BytesTotal: 2}, width: width, height: height},
			{screen: screenInstalled, installed: values, status: "Installed blender", width: width, height: height},
			{screen: screenDetails, detail: detail, width: width, height: height},
		}
		for _, m := range scenarios {
			lines := strings.Split(strings.TrimSuffix(m.View().Content, "\n"), "\n")
			for i, line := range lines {
				if displayWidth(line) > width {
					t.Fatalf("%dx%d screen %d: width overflow line %d: %q", width, height, m.screen, i, line)
				}
			}
			if len(lines) > height {
				t.Fatalf("%dx%d screen %d: rendered %d lines exceeds height, footer lost", width, height, m.screen, len(lines))
			}
		}
	}
}

func TestTightHeaderProvidesListRowsWithinHeight(t *testing.T) {
	values := make([]app.Application, 40)
	for i := range values {
		values[i] = app.Application{ID: string(rune('a' + i)), Name: "Application"}
	}
	for _, size := range [][2]int{{80, 24}, {60, 18}, {80, 12}} {
		width, height := size[0], size[1]
		m := model{screen: screenInstalled, installed: values, width: width, height: height}
		rows := m.listRows()
		// The rendered list must fit entirely within the height budget.
		if rows > height {
			t.Fatalf("%dx%d: listRows %d exceeds height", width, height, rows)
		}
		// Chrome (title, header, separator, blank-before-footer, footer) leaves
		// room for the list without overflowing the height.
		if rows+len(footerLines(m.footer(), width))+2 > height {
			t.Fatalf("%dx%d: header+list+footer overflow height", width, height)
		}
	}
}

func TestSearchCancellationViaContext(t *testing.T) {
	service := &fakeService{
		applications:   []app.Application{{ID: "blender", Name: "Blender"}},
		blockSearch:    true,
		searchStarted:  make(chan struct{}),
		searchCanceled: make(chan struct{}),
	}
	m := model{ctx: context.Background(), service: service, screen: screenAvailable, available: service.applications}
	// Begin searching then submit the query.
	updated, _ := m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	modelBusy := updated.(model)
	if command == nil || modelBusy.busy != "Searching" || modelBusy.opCancel == nil {
		t.Fatalf("search did not start as cancellable: %#v", modelBusy)
	}
	var result tea.Msg
	done := make(chan struct{})
	go func() {
		result = command()
		close(done)
	}()
	select {
	case <-service.searchStarted:
	case <-time.After(time.Second):
		t.Fatal("search did not reach the service")
	}
	// Esc cancels the active search.
	cancelled, _ := modelBusy.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	select {
	case <-service.searchCanceled:
	case <-time.After(time.Second):
		t.Fatal("active search was not cancelled via context")
	}
	<-done
	msg, ok := result.(searchMsg)
	if !ok || !msg.cancelled {
		t.Fatalf("search result = %#v, want cancelled searchMsg", result)
	}
	finished, _ := cancelled.(model).Update(result)
	modelDone := finished.(model)
	if modelDone.busy != "" || modelDone.err != nil || modelDone.opCancel != nil {
		t.Fatalf("search cancellation left stale state: %#v", modelDone)
	}
	// The prior list must be preserved (no scary generic failure).
	if len(modelDone.available) != 1 {
		t.Fatalf("search cancellation lost available list: %#v", modelDone.available)
	}
	if !strings.Contains(modelDone.View().Content, "cancelled") {
		t.Fatalf("search cancellation not indicated: %q", modelDone.View().Content)
	}
}

func TestVersionsCancellationViaContext(t *testing.T) {
	service := &fakeService{
		applications:     []app.Application{{ID: "blender", Name: "Blender", InstalledVersion: "5.2.0"}},
		blockVersions:    true,
		versionsStarted:  make(chan struct{}),
		versionsCanceled: make(chan struct{}),
	}
	m := model{ctx: context.Background(), service: service, screen: screenInstalled, installed: service.applications}
	// Open details then request versions.
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	updated, command := updated.(model).Update(tea.KeyPressMsg{Code: 'v', Text: "v"})
	modelBusy := updated.(model)
	if command == nil || modelBusy.busy != "Loading versions" || modelBusy.opCancel == nil {
		t.Fatalf("versions did not start as cancellable: %#v", modelBusy)
	}
	var result tea.Msg
	done := make(chan struct{})
	go func() {
		result = command()
		close(done)
	}()
	select {
	case <-service.versionsStarted:
	case <-time.After(time.Second):
		t.Fatal("versions did not reach the service")
	}
	// Esc cancels the active versions load.
	cancelled, _ := modelBusy.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	select {
	case <-service.versionsCanceled:
	case <-time.After(time.Second):
		t.Fatal("active versions load was not cancelled via context")
	}
	<-done
	msg, ok := result.(versionsMsg)
	if !ok || !msg.cancelled {
		t.Fatalf("versions result = %#v, want cancelled versionsMsg", result)
	}
	finished, _ := cancelled.(model).Update(result)
	modelDone := finished.(model)
	if modelDone.busy != "" || modelDone.err != nil || modelDone.opCancel != nil {
		t.Fatalf("versions cancellation left stale state: %#v", modelDone)
	}
	// Should stay on the prior (details) screen, not jump to versions.
	if modelDone.screen != screenDetails {
		t.Fatalf("versions cancellation changed screen to %v, want details", modelDone.screen)
	}
	if !strings.Contains(modelDone.View().Content, "cancelled") {
		t.Fatalf("versions cancellation not indicated: %q", modelDone.View().Content)
	}
}
