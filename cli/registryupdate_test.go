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
	report app.RegistryUpdateReport
	err    error
	calls  int
}

func (s *registryUpdateService) UpdateRegistry(context.Context, app.ProgressSink) (app.RegistryUpdateReport, error) {
	s.calls++
	return s.report, s.err
}

func TestRegistryUpdatePrintsHumanAndJSONReports(t *testing.T) {
	report := app.RegistryUpdateReport{SchemaVersion: 1, Revision: "snapshot", Results: []app.RegistryUpdateResult{
		{App: "godot", Platform: "linux-arm64", Status: "candidates", Candidates: []freshness.Candidate{{App: "godot", Channel: "stable", Version: "2.0", UpstreamURL: "https://github.com/godotengine/godot/releases/tag/2.0"}}},
		{App: "foo", Platform: "linux-amd64", Status: "skipped", Reason: "no_approved_upstream", Candidates: []freshness.Candidate{}},
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
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got.Results) != 2 || got.Revision != "snapshot" {
				t.Fatalf("JSON=%s err=%v", out.String(), err)
			}
		} else if !strings.Contains(out.String(), "godot linux-arm64: candidates") || !strings.Contains(out.String(), "stable 2.0") || !strings.Contains(out.String(), "skipped (no_approved_upstream)") {
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
	for _, args := range [][]string{{"registry", "update", "godot"}, {"registry", "update", "--unknown"}, {"registry", "update", "--offline"}} {
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
