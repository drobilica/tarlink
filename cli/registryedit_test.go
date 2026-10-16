package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

type onboardingFixture struct {
	result  app.RegistryAddResult
	options app.RegistryAddOptions
	calls   int
}

func (f *onboardingFixture) InspectRegistry(context.Context, app.RegistryInspectOptions) (app.RegistryInspectionResult, error) {
	return app.RegistryInspectionResult{}, errors.New("unexpected inspection")
}
func (f *onboardingFixture) AddRegistry(_ context.Context, options app.RegistryAddOptions) (app.RegistryAddResult, error) {
	f.calls++
	f.options = options
	return f.result, nil
}

func cliCandidate(t *testing.T, id string) app.RegistryAddResult {
	t.Helper()
	result, err := app.CompleteRegistryCandidate(app.RegistryCandidate{Repository: "owner/" + id, Release: "v1", URL: "https://example.invalid/" + id + ".zip", Platform: "linux-amd64", Archive: "zip", SHA256: strings.Repeat("a", 64), Executable: id}, app.RegistryAddOptions{ID: id, Name: id, Categories: []string{"utilities"}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRegistryAddJSONWritesRequestedOutputAndRefusesOverwrite(t *testing.T) {
	service := &onboardingFixture{result: cliCandidate(t, "demo")}
	path := filepath.Join(t.TempDir(), "candidate.yaml")
	var out, diagnostics bytes.Buffer
	runner := Runner{Registry: RegistryTools{Onboarding: service}, Stdout: &out, Stderr: &diagnostics}
	args := []string{"registry", "add", "owner/demo", "--non-interactive", "--json", "--output", path, "--executable", "demo", "--no-icon"}
	if code := runner.Run(context.Background(), args); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, &diagnostics)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, service.result.YAML) {
		t.Fatalf("output=%s err=%v", data, err)
	}
	var result app.RegistryAddResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Output != path {
		t.Fatalf("JSON=%s err=%v", &out, err)
	}
	if service.options.Executable != "demo" || !service.options.NoIcon {
		t.Fatalf("options=%+v", service.options)
	}
	out.Reset()
	if code := runner.Run(context.Background(), args); code == 0 {
		t.Fatal("overwrote existing candidate")
	}
	data, _ = os.ReadFile(path)
	if !bytes.Equal(data, service.result.YAML) {
		t.Fatal("existing output changed")
	}
}

func TestRegistryAddAndWithdrawalUseLocalMaintainerOnly(t *testing.T) {
	root := t.TempDir()
	seed := cliCandidate(t, "seed")
	seedDir := filepath.Join(root, "apps", "seed")
	if err := os.MkdirAll(seedDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedDir, "manifest.yaml"), seed.YAML, 0644); err != nil {
		t.Fatal(err)
	}
	service := &onboardingFixture{result: cliCandidate(t, "demo")}
	var out, diagnostics bytes.Buffer
	runner := Runner{Registry: RegistryTools{Onboarding: service, Editing: &app.Maintainer{}}, Stdout: &out, Stderr: &diagnostics}
	args := []string{"registry", "add", "owner/demo", "--non-interactive", "--registry", root, "--json", "--dry-run"}
	if code := runner.Run(context.Background(), args); code != 0 {
		t.Fatalf("preview exit=%d stderr=%s", code, &diagnostics)
	}
	var added app.RegistryAddResult
	if err := json.Unmarshal(out.Bytes(), &added); err != nil || added.Edit == nil || added.Edit.Written || !added.Edit.DryRun {
		t.Fatalf("preview=%s err=%v", &out, err)
	}
	path := filepath.Join(root, "apps", "demo", "manifest.yaml")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("dry run created manifest: %v", err)
	}
	out.Reset()
	if code := runner.Run(context.Background(), args[:len(args)-1]); code != 0 {
		t.Fatalf("add exit=%d stderr=%s", code, &diagnostics)
	}
	if err := json.Unmarshal(out.Bytes(), &added); err != nil || added.Edit == nil || !added.Edit.Written {
		t.Fatalf("add JSON=%s err=%v", &out, err)
	}
	for _, action := range []string{"disable", "enable"} {
		out.Reset()
		if !RegistryMaintainerCommand([]string{"registry", action}) {
			t.Fatalf("%s constructs runtime", action)
		}
		if code := runner.Run(context.Background(), []string{"registry", action, "demo", "--registry", root, "--json"}); code != 0 {
			t.Fatalf("%s exit=%d stderr=%s", action, code, &diagnostics)
		}
		var report app.RegistryEditReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Action != action || !report.Written {
			t.Fatalf("%s JSON=%s err=%v", action, &out, err)
		}
	}
}

func TestRegistryAddNeedsInputAndInvalidFlagsNeverWrite(t *testing.T) {
	for _, kind := range []string{"needs-input", "conflicting-destinations", "conflicting-icons", "dry-output", "missing-root"} {
		t.Run(kind, func(t *testing.T) {
			service := &onboardingFixture{result: cliCandidate(t, "demo")}
			path := filepath.Join(t.TempDir(), "candidate.yaml")
			args := []string{"registry", "add", "owner/demo", "--non-interactive", "--json", "--output", path}
			if kind == "needs-input" {
				service.result = app.RegistryAddResult{Status: "needs-input", Required: []app.RegistryRequiredInput{{Field: "artifact", Reason: "unsupported_arch"}}}
			}
			switch kind {
			case "conflicting-destinations":
				args = append(args, "--registry", "/registry")
			case "conflicting-icons":
				args = append(args, "--icon", "foo.svg", "--no-icon")
			case "dry-output":
				args = append(args, "--dry-run")
			case "missing-root":
				args = []string{"registry", "disable", "demo", "--json"}
			}
			var out, diagnostics bytes.Buffer
			code := (Runner{Registry: RegistryTools{Onboarding: service, Editing: &app.Maintainer{}}, Stdout: &out, Stderr: &diagnostics}).Run(context.Background(), args)
			if code != 2 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &diagnostics)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failure wrote output: %v", err)
			}
			if kind != "needs-input" && service.calls != 0 {
				t.Fatal("invalid arguments performed discovery")
			}
			if kind == "needs-input" && !json.Valid(out.Bytes()) {
				t.Fatalf("needs-input is not JSON: %s", &out)
			}
		})
	}
}
