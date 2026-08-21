package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/research"
)

type fakeService struct {
	applications   []app.Application
	uninstalled    []string
	uninstalledAll bool
	validatedRoot  string
	tarlinkVersion app.TarLinkVersion
	upgradeValue   app.TarLinkVersion
	pathConflicts  []app.PathConflict
	pathChecked    string
	installed      []string
	doctorReport   app.DoctorReport
}

func (f *fakeService) Install(_ context.Context, appID string, _ app.ProgressSink) (app.Result, error) {
	f.installed = append(f.installed, appID)
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
func (f *fakeService) CheckInstallPath(appID string) ([]app.PathConflict, error) {
	f.pathChecked = appID
	return f.pathConflicts, nil
}
func (f *fakeService) Doctor(context.Context) (app.DoctorReport, error) {
	return f.doctorReport, nil
}

type freshnessService struct {
	fakeService
	report freshness.Report
}

type researchService struct {
	fakeService
	result  app.ResearchResult
	err     error
	options app.ResearchOptions
}

func (f *researchService) Research(_ context.Context, options app.ResearchOptions) (app.ResearchResult, error) {
	f.options = options
	return f.result, f.err
}

func TestRegistryProvenanceJSONSelectors(t *testing.T) {
	var out bytes.Buffer
	service := &researchService{result: app.ResearchResult{Repository: "owner/repo", Release: research.Release{ID: 10, Tag: "v1"}, Asset: research.Asset{ID: 20, Name: "linux.zip", Digest: "sha256:abc"}, Provenance: research.Provenance{Verdict: research.Acceptable, Algorithm: "sha256", Digest: "sha256:abc", Message: "ok"}}}
	code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "provenance", "owner/repo", "--release", "v1", "--asset", "linux.zip", "--refresh", "--json"})
	if code != 0 {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"release"`)) || service.options.Release != "v1" || !service.options.Refresh {
		t.Fatalf("output=%q options=%+v", out.String(), service.options)
	}
}

func TestRegistryResearchJSONErrorIsJSONOnly(t *testing.T) {
	var out bytes.Buffer
	service := &researchService{err: &app.ResearchFailure{ReasonCode: "ASSET_NOT_FOUND", Err: errors.New("asset not found")}}
	code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "owner/repo", "--json"})
	if code == 0 || !bytes.Contains(out.Bytes(), []byte(`"reason_code":"ASSET_NOT_FOUND"`)) || !bytes.Contains(out.Bytes(), []byte(`"error"`)) {
		t.Fatalf("code=%d output=%q", code, out.String())
	}
}

func TestRegistryResearchProviderFailureIsStructuredErrorResult(t *testing.T) {
	var out bytes.Buffer
	service := &researchService{
		result: app.ResearchResult{
			Repository: "owner/repo",
			Provenance: research.Provenance{Verdict: research.Error, ReasonCode: "RATE_LIMITED", Message: "GitHub API rate limit exceeded"},
			Status:     "ERROR",
			Error:      &app.ResearchError{Kind: research.APIErrorRateLimited, HTTPStatus: 429, ReasonCode: "RATE_LIMITED", Message: "GitHub API rate limit exceeded"},
		},
		err: &app.ResearchFailure{ReasonCode: "RATE_LIMITED", Kind: research.APIErrorRateLimited, HTTPStatus: 429, Err: errors.New("GitHub API rate limit exceeded")},
	}
	code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "provenance", "owner/repo", "--json"})
	if code == 0 {
		t.Fatal("provider failure returned success")
	}
	var got app.ResearchResult
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v (%s)", err, out.String())
	}
	if got.Provenance.Verdict != research.Error || got.Status != "ERROR" || got.Error == nil || got.Error.Kind != research.APIErrorRateLimited {
		t.Fatalf("unexpected structured failure: %+v", got)
	}
}

func TestRegistryResearchJSONHasStableResultShape(t *testing.T) {
	for _, verdict := range []research.Verdict{research.Acceptable, research.Rejected, research.Error} {
		var out bytes.Buffer
		service := &researchService{result: app.ResearchResult{
			Repository: "owner/repo", Release: research.Release{ID: 10, Tag: "v1"},
			Asset:      research.Asset{ID: 20, Name: "linux.zip", Size: 42},
			Provenance: research.Provenance{Verdict: verdict, ReasonCode: "TEST", Message: "fixture"},
			Status:     map[research.Verdict]string{research.Acceptable: "READY_FOR_REVIEW", research.Rejected: "BLOCKED", research.Error: "ERROR"}[verdict],
		}}
		if code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "provenance", "owner/repo", "--json"}); code != 0 {
			t.Fatalf("verdict %s code=%d", verdict, code)
		}
		var got map[string]any
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("verdict %s invalid JSON: %v", verdict, err)
		}
		for _, field := range []string{"repository", "release", "asset", "provenance", "status"} {
			if _, ok := got[field]; !ok {
				t.Fatalf("verdict %s missing %q: %s", verdict, field, out.String())
			}
		}
	}
}

func TestRegistryInspectHumanOutputIncludesArchiveFacts(t *testing.T) {
	var out bytes.Buffer
	service := &researchService{result: app.ResearchResult{
		Repository: "owner/repo", Release: research.Release{ID: 10, Tag: "v1"}, Asset: research.Asset{ID: 20, Name: "linux.tar.gz"},
		Provenance: research.Provenance{Verdict: research.Acceptable, Algorithm: "sha256", Digest: "abc", Message: "ok"}, Status: "READY_FOR_REVIEW",
		Inspection: &research.Inspection{ArtifactType: "tar.gz", Executables: []string{"app"}, Nested: []string{"data.zip"}},
	}}
	if code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "inspect", "owner/repo"}); code != 0 {
		t.Fatalf("inspect code=%d output=%q", code, out.String())
	}
	for _, field := range []string{"Executables: app", "Nested archives: data.zip"} {
		if !bytes.Contains(out.Bytes(), []byte(field)) {
			t.Fatalf("missing %q in output %q", field, out.String())
		}
	}
}

func (f *freshnessService) Freshness(context.Context, string) (freshness.Report, error) {
	return f.report, nil
}

func TestRegistryFreshnessIsReadOnlyAndSupportsJSON(t *testing.T) {
	var out bytes.Buffer
	service := &freshnessService{report: freshness.Report{Candidates: []freshness.Candidate{{App: "pcsx2", Repository: "PCSX2/pcsx2", Channel: "nightly", Version: "2.7.519"}}}}
	if code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "freshness", "pcsx2", "--json"}); code != 0 {
		t.Fatalf("freshness code=%d output=%q", code, out.String())
	}
	if !bytes.Contains(out.Bytes(), []byte(`"version":"2.7.519"`)) {
		t.Fatalf("freshness JSON=%q", out.String())
	}
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
	if code != 0 || out.String() != "[{\"id\":\"blender\",\"name\":\"Blender\",\"summary\":\"\",\"homepage\":\"\",\"categories\":null,\"registry_version\":\"5.2.0\",\"pinned\":false,\"update_available\":false}]\n" {
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

func TestInstallSelectorPreflightUsesApplicationID(t *testing.T) {
	service := &fakeService{}
	var out, errOut bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: &errOut}
	if code := runner.Run(context.Background(), []string{"install", "blender@nightly", "--force-path"}); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
	if service.pathChecked != "blender" {
		t.Fatalf("PATH preflight checked %q", service.pathChecked)
	}
	if len(service.installed) != 1 || service.installed[0] != "blender@nightly" {
		t.Fatalf("install selectors=%v", service.installed)
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
