package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

type fakeService struct {
	applications   []app.Application
	uninstalled    []string
	uninstalledAll bool
	validatedRoot  string
	tarlinkVersion app.TarLinkVersion
	upgradeValue   app.TarLinkVersion
	pathConflicts  []app.PathConflict
	doctorReport   app.DoctorReport
}

func (f *fakeService) Install(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{AppID: "blender", Version: "5.2.0"}, nil
}
func (f *fakeService) Update(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, errors.New("unused")
}
func (f *fakeService) UpdateAll(context.Context, app.ProgressSink) (app.UpdateAllResult, error) {
	return app.UpdateAllResult{}, errors.New("unused")
}
func (f *fakeService) Uninstall(_ context.Context, appID string, _ app.ProgressSink) error {
	f.uninstalled = append(f.uninstalled, appID)
	return nil
}
func (f *fakeService) UninstallAll(context.Context, app.ProgressSink) error {
	f.uninstalledAll = true
	return nil
}
func (f *fakeService) Rollback(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, errors.New("unused")
}
func (f *fakeService) List(context.Context) ([]app.Application, error) { return f.applications, nil }
func (f *fakeService) Info(context.Context, string) (app.Application, error) {
	return f.applications[0], nil
}
func (f *fakeService) Search(context.Context, string) ([]app.Application, error) {
	return f.applications, nil
}
func (f *fakeService) Versions(context.Context, string) ([]app.Version, error) {
	return []app.Version{{Version: "1", Status: "current"}}, nil
}
func (f *fakeService) SyncRegistry(context.Context, app.ProgressSink) error { return nil }
func (f *fakeService) ValidateRegistry(_ context.Context, root string) error {
	f.validatedRoot = root
	return nil
}
func (f *fakeService) CheckTarLinkVersion(context.Context) (app.TarLinkVersion, error) {
	return f.tarlinkVersion, nil
}
func (f *fakeService) CheckTarLinkVersionFresh(context.Context) (app.TarLinkVersion, error) {
	return f.tarlinkVersion, nil
}
func (f *fakeService) UpgradeTarLink(context.Context, app.ProgressSink) (app.TarLinkVersion, error) {
	if f.upgradeValue.Current != "" {
		return f.upgradeValue, nil
	}
	return app.TarLinkVersion{}, errors.New("unused")
}
func (f *fakeService) CheckInstallPath(string) ([]app.PathConflict, error) {
	return f.pathConflicts, nil
}
func (f *fakeService) Doctor(context.Context) (app.DoctorReport, error) {
	return f.doctorReport, nil
}

func TestDoctorExitStatusDistinguishesWarningsAndErrors(t *testing.T) {
	warning := &fakeService{doctorReport: app.DoctorReport{Warnings: 1}}
	if code := (Runner{Service: warning, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"doctor"}); code != 0 {
		t.Fatalf("warning-only doctor code=%d", code)
	}
	errorReport := &fakeService{doctorReport: app.DoctorReport{Errors: 1}}
	if code := (Runner{Service: errorReport, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"doctor"}); code == 0 {
		t.Fatal("doctor integrity error returned success")
	}
}

func TestNoArgumentsLaunchesTUI(t *testing.T) {
	var out bytes.Buffer
	launched := false
	runner := Runner{Stdout: &out, Stderr: &bytes.Buffer{}, LaunchTUI: func(context.Context, app.Service, io.Writer, io.Writer) error {
		launched = true
		return nil
	}}
	if code := runner.Run(context.Background(), nil); code != 0 || !launched {
		t.Fatalf("code=%d launched=%t output=%q", code, launched, out.String())
	}
}

func TestTUICommandIsRejected(t *testing.T) {
	runner := Runner{Service: &fakeService{}, Stdout: io.Discard, Stderr: io.Discard}
	if code := runner.Run(context.Background(), []string{"tui"}); code != exitInvalidArguments {
		t.Fatalf("tui command code=%d", code)
	}
}

func TestHelpDoesNotAdvertiseTUICommand(t *testing.T) {
	var out bytes.Buffer
	if code := (Runner{Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"help"}); code != 0 || bytes.Contains(out.Bytes(), []byte("tarlink tui")) {
		t.Fatalf("code=%d help=%q", code, out.String())
	}
}

func TestJSONOutputContainsJSONOnly(t *testing.T) {
	var out, errOut bytes.Buffer
	service := &fakeService{applications: []app.Application{{ID: "blender", Name: "Blender", RegistryVersion: "5.2.0"}}}
	code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), []string{"list", "--json"})
	if code != 0 || out.String() != "[{\"id\":\"blender\",\"name\":\"Blender\",\"summary\":\"\",\"homepage\":\"\",\"categories\":null,\"registry_version\":\"5.2.0\",\"update_available\":false}]\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestGameDataRequirementIsPresented(t *testing.T) {
	var out bytes.Buffer
	service := &fakeService{applications: []app.Application{{ID: "banjo-recompiled", Name: "Banjo", Requirements: []string{"original-game-data"}}}}
	runner := Runner{Service: service, Stdout: &out, Stderr: io.Discard}
	if code := runner.Run(context.Background(), []string{"list"}); code != 0 || !bytes.Contains(out.Bytes(), []byte("[GAME DATA]")) {
		t.Fatalf("list output = %q", out.String())
	}
	out.Reset()
	if code := runner.Run(context.Background(), []string{"info", "banjo-recompiled"}); code != 0 || !bytes.Contains(out.Bytes(), []byte("Requires:     Original game data")) {
		t.Fatalf("info output = %q", out.String())
	}
	out.Reset()
	if code := runner.Run(context.Background(), []string{"info", "banjo-recompiled", "--json"}); code != 0 || !bytes.Contains(out.Bytes(), []byte(`"requirements":["original-game-data"]`)) {
		t.Fatalf("json output = %q", out.String())
	}
}

func TestVersionNoticeUsesStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	service := &fakeService{applications: []app.Application{{ID: "blender"}}, tarlinkVersion: app.TarLinkVersion{Current: "0.4.2", Latest: "0.5.0", UpgradeAvailable: true}}
	if code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), []string{"list", "--json"}); code != 0 || !bytes.Contains(out.Bytes(), []byte("[")) || errOut.Len() != 0 {
		t.Fatalf("JSON output was contaminated: code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	errOut.Reset()
	if code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), []string{"list"}); code != 0 || !bytes.Contains(errOut.Bytes(), []byte("tarlink upgrade")) {
		t.Fatalf("notice was not sent to stderr: code=%d stderr=%q", code, errOut.String())
	}
}

func TestUpgradeCommandDelegatesAndReportsCurrent(t *testing.T) {
	var out bytes.Buffer
	service := &fakeService{upgradeValue: app.TarLinkVersion{Current: "0.5.0", Latest: "0.5.0"}}
	if code := (Runner{Service: service, Stdout: &out, Stderr: &bytes.Buffer{}}).Run(context.Background(), []string{"upgrade"}); code != 0 || out.String() != "TarLink 0.5.0 is already up to date.\n" {
		t.Fatalf("upgrade output: code=%d output=%q", code, out.String())
	}
}

func TestInvalidArgumentsHaveStableExit(t *testing.T) {
	var errOut bytes.Buffer
	code := (Runner{Service: &fakeService{}, Stdout: &bytes.Buffer{}, Stderr: &errOut}).Run(context.Background(), []string{"info"})
	if code != exitInvalidArguments || errOut.Len() == 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}

func TestUninstallCommands(t *testing.T) {
	service := &fakeService{}
	var out, errOut bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: &errOut}
	if code := runner.Run(context.Background(), []string{"uninstall", "demo"}); code != 0 {
		t.Fatalf("uninstall code=%d stderr=%q", code, errOut.String())
	}
	if len(service.uninstalled) != 1 || service.uninstalled[0] != "demo" || out.String() != "Uninstalled demo\n" {
		t.Fatalf("uninstall calls=%v output=%q", service.uninstalled, out.String())
	}
	out.Reset()
	if code := runner.Run(context.Background(), []string{"uninstall", "--all"}); code != 0 {
		t.Fatalf("uninstall all code=%d stderr=%q", code, errOut.String())
	}
	if !service.uninstalledAll || out.String() != "Uninstalled all applications\n" {
		t.Fatalf("uninstall all called=%t output=%q", service.uninstalledAll, out.String())
	}
}

func TestLegacyRemoveCommandIsRejected(t *testing.T) {
	runner := Runner{Service: &fakeService{}, Stdout: io.Discard, Stderr: io.Discard}
	if code := runner.Run(context.Background(), []string{"remove", "demo"}); code != exitInvalidArguments {
		t.Fatalf("legacy remove command code=%d", code)
	}
}

func TestInstallPathConflictsRequireAcknowledgement(t *testing.T) {
	service := &fakeService{applications: []app.Application{{ID: "blender"}}}
	var out, errOut bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: &errOut}

	// A shadowing conflict refuses install unless acknowledged.
	service.pathConflicts = []app.PathConflict{{Type: "shadowed", Executable: "blender", Directory: "/usr/bin", Candidate: "/usr/bin/blender"}}
	if code := runner.Run(context.Background(), []string{"install", "blender"}); code != exitConflict || !bytes.Contains(errOut.Bytes(), []byte("--force-path")) {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	// Acknowledging proceeds with install.
	errOut.Reset()
	if code := runner.Run(context.Background(), []string{"install", "blender", "--force-path"}); code != 0 || out.String() != "Installed blender 5.2.0\n" {
		t.Fatalf("acknowledged install code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
	// Without conflicts, install proceeds without the flag.
	service.pathConflicts = nil
	out.Reset()
	errOut.Reset()
	if code := runner.Run(context.Background(), []string{"install", "blender"}); code != 0 {
		t.Fatalf("clean install code=%d stderr=%q", code, errOut.String())
	}
}

func TestRegistryValidateCommand(t *testing.T) {
	service := &fakeService{}
	var out, errOut bytes.Buffer
	code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), []string{"registry", "validate", "/registry"})
	if code != 0 || service.validatedRoot != "/registry" || out.String() != "Registry is valid\n" {
		t.Fatalf("code=%d root=%q stdout=%q stderr=%q", code, service.validatedRoot, out.String(), errOut.String())
	}
}
