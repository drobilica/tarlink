package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

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
func (f *fakeService) Search(context.Context, string) ([]app.Application, error) {
	return f.applications, nil
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
	if !strings.Contains(view.Content, "Blender") || !strings.Contains(view.Content, "AVAILABLE / SEARCH") {
		t.Fatalf("unexpected view: %q", view.Content)
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
