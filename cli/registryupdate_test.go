package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/freshness"
)

type registryUpdateService struct {
	fakeService
	report  app.RegistryUpdateReport
	err     error
	calls   int
	options app.RegistryUpdateOptions
}

func (s *registryUpdateService) UpdateRegistry(_ context.Context, options app.RegistryUpdateOptions, _ app.ProgressSink) (app.RegistryUpdateReport, error) {
	s.calls++
	s.options = options
	return s.report, s.err
}

func TestRegistryUpdatePrintsHumanAndJSONReports(t *testing.T) {
	report := app.RegistryUpdateReport{SchemaVersion: 1, Revision: "snapshot", Summary: app.RegistryUpdateSummary{Total: 2, Candidates: 1, Skipped: 1, SkipReasons: map[string]int{"unreviewed_repository": 1}}, Results: []app.RegistryUpdateResult{
		{App: "godot", Platform: "linux-arm64", Status: "candidates", Candidates: []freshness.Candidate{{App: "godot", Channel: "stable", Version: "2.0", UpstreamURL: "https://github.com/godotengine/godot/releases/tag/2.0"}}},
		{App: "foo", Platform: "linux-amd64", Status: "skipped", Reason: "unreviewed_repository", Detail: "GitHub source needs a reviewed rule", Candidates: []freshness.Candidate{}},
	}}
	for _, jsonOutput := range []bool{false, true} {
		service := &registryUpdateService{report: report}
		var out, errOut bytes.Buffer
		args := []string{"registry", "update"}
		if jsonOutput {
			args = append(args, "--json")
		}
		code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), args)
		if code != 0 || service.calls != 1 || errOut.Len() != 0 {
			t.Fatalf("code=%d calls=%d stderr=%q", code, service.calls, errOut.String())
		}
		if jsonOutput {
			var got app.RegistryUpdateReport
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Results) != 2 || got.Revision != "snapshot" || got.Summary.Total != 2 || got.Summary.SkipReasons["unreviewed_repository"] != 1 {
				t.Fatalf("JSON=%s err=%v", out.String(), err)
			}
		} else if !strings.Contains(out.String(), "Results: 2 total, 1 with candidates, 0 without candidates, 1 skipped, 0 errors.") || !strings.Contains(out.String(), "godot linux-arm64: candidates") || !strings.Contains(out.String(), "stable 2.0") || !strings.Contains(out.String(), "skipped (unreviewed_repository): GitHub source needs a reviewed rule") {
			t.Fatalf("output=%s", out.String())
		}
	}
}

func TestRegistryUpdatePreservesPartialReportAndFails(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		service := &registryUpdateService{report: app.RegistryUpdateReport{SchemaVersion: 1, Results: []app.RegistryUpdateResult{{App: "godot", Platform: "linux-amd64", Status: "error", Reason: "lookup_failed", Error: "API unavailable", Candidates: []freshness.Candidate{}}}}, err: errors.New("API unavailable")}
		var out, errOut bytes.Buffer
		args := []string{"registry", "update"}
		if jsonOutput {
			args = append(args, "--json")
		}
		if code := (Runner{Service: service, Stdout: &out, Stderr: &errOut}).Run(context.Background(), args); code == 0 {
			t.Fatal("failed scan exited successfully")
		}
		if !strings.Contains(out.String(), "godot") || !strings.Contains(errOut.String(), "API unavailable") || strings.Contains(out.String(), "Scan complete") {
			t.Fatalf("stdout=%s stderr=%s", out.String(), errOut.String())
		}
		if jsonOutput && !json.Valid(out.Bytes()) {
			t.Fatalf("partial report is not JSON: %s", out.String())
		}
	}
}

func TestRegistryUpdateRejectsArgumentsAndOfflineBeforeScanning(t *testing.T) {
	for _, args := range [][]string{{"registry", "update", "godot"}, {"registry", "update", "--unknown"}, {"registry", "update", "--offline"}, {"registry", "update", "-n", "0"}, {"registry", "update", "-n", "-1"}, {"registry", "update", "-n", "bad"}} {
		service := &registryUpdateService{}
		code := (Runner{Service: service, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), args)
		if code != exitInvalidArguments || service.calls != 0 {
			t.Fatalf("args=%v code=%d calls=%d", args, code, service.calls)
		}
	}
	if RegistryMaintainerCommand([]string{"registry", "update"}) {
		t.Fatal("registry update needs the official registry sync service")
	}
}

func TestRegistryUpdatePassesLimitForTextAndJSON(t *testing.T) {
	for _, args := range [][]string{{"registry", "update", "-n", "5"}, {"registry", "update", "--number", "5", "--json"}} {
		service := &registryUpdateService{}
		code := (Runner{Service: service, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), args)
		if code != 0 || service.calls != 1 || service.options.Limit != 5 {
			t.Fatalf("args=%v code=%d options=%+v calls=%d", args, code, service.options, service.calls)
		}
	}
}

type registryApplyFake struct {
	options app.RegistryApplyOptions
	calls   int
	err     error
}

func (s *registryApplyFake) ApplyRegistryUpdates(_ context.Context, options app.RegistryApplyOptions) (app.RegistryApplyReport, error) {
	s.options = options
	s.calls++
	return app.RegistryApplyReport{SchemaVersion: 1, DryRun: options.DryRun, Results: []app.RegistryApplyResult{{App: "godot", Status: "would-update", Versions: []string{"stable:2.0"}}}}, s.err
}
func TestRegistryApplyCLI(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		service := &registryApplyFake{}
		var out bytes.Buffer
		args := []string{"registry", "update", "--apply", "--registry", "/checkout", "--dry-run"}
		if jsonOutput {
			args = append(args, "--json")
		}
		if !RegistryMaintainerCommand(args) {
			t.Fatal("local apply constructed runtime")
		}
		code := (Runner{Registry: RegistryTools{Apply: service}, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), args)
		if code != 0 || service.calls != 1 || service.options.Root != "/checkout" || !service.options.DryRun {
			t.Fatalf("code=%d service=%+v", code, service)
		}
		if jsonOutput && !json.Valid(out.Bytes()) {
			t.Fatalf("JSON=%s", out.String())
		}
		if !strings.Contains(out.String(), "would-update") {
			t.Fatalf("output=%s", out.String())
		}
	}
	for _, args := range [][]string{
		{"registry", "update", "--apply"}, {"registry", "update", "--dry-run"}, {"registry", "update", "--registry", "/checkout"},
		{"registry", "update", "--apply", "--registry", "/checkout", "-n", "5"}, {"registry", "update", "--apply", "--registry", "/checkout", "--offline"},
	} {
		service := &registryApplyFake{}
		code := (Runner{Registry: RegistryTools{Apply: service}, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), args)
		if code != exitInvalidArguments || service.calls != 0 {
			t.Fatalf("args=%v code=%d calls=%d", args, code, service.calls)
		}
	}
}
func TestRegistryApplyCLIReturnsPartialFailure(t *testing.T) {
	service := &registryApplyFake{err: errors.New("failed artifact")}
	var out bytes.Buffer
	code := (Runner{Registry: RegistryTools{Apply: service}, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "update", "--apply", "--registry", "/checkout", "--json"})
	if code == 0 || !json.Valid(out.Bytes()) {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
}

func TestRegistryUpdateAppSelector(t *testing.T) {
	scan := &registryUpdateService{}
	code := (Runner{Service: scan, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "update", "--app", "godot"})
	if code != 0 || scan.options.App != "godot" {
		t.Fatalf("%d %+v", code, scan.options)
	}
	apply := &registryApplyFake{}
	var out bytes.Buffer
	code = (Runner{Registry: RegistryTools{Apply: apply}, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), []string{"registry", "update", "--apply", "--registry", "/checkout", "--app", "godot"})
	if code != 0 || apply.options.App != "godot" || !strings.Contains(out.String(), "Results:") {
		t.Fatalf("%d %+v %s", code, apply.options, out.String())
	}
}
