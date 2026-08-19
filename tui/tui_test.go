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
	applications    []app.Application
	installedValues []app.Application
	rolledBack      string
	uninstalled     string
	uninstallErr    error
	installProgress []app.Progress
	tarlinkVersion  app.TarLinkVersion
}

func (f *fakeService) Install(_ context.Context, _ string, sink app.ProgressSink) (app.Result, error) {
	for _, event := range f.installProgress {
		if sink != nil {
			sink(event)
		}
	}
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
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
func (f *fakeService) Search(_ context.Context, query string) ([]app.Application, error) {
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
func (f *fakeService) Versions(context.Context, string) ([]app.Version, error) {
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
	if got := m.footer(); got != "q Quit" {
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
	updated, _ := m.Update(m.searchCmd()())
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
	} {
		if got := applicationStatus(tc.value); got != tc.want {
			t.Errorf("status = %q, want %q", got, tc.want)
		}
	}
	if strings.Contains(installedLabel(app.Application{}), "Install") || strings.Contains(installedLabel(app.Application{InstalledVersion: "1.0"}), "current") {
		t.Fatal("action-oriented state label regressed")
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
		t.Fatal("install did not start")
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
