package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

type updatePlanFakeService struct {
	fakeService
	plan      app.UpdatePlan
	selection []string
	applied   bool
}

func (f *updatePlanFakeService) PlanUpdates(_ context.Context, selection []string) (app.UpdatePlan, error) {
	f.selection = selection
	return f.plan, nil
}
func (f *updatePlanFakeService) ApplyUpdatePlan(context.Context, app.UpdatePlan, app.ProgressSink) (app.UpdateAllResult, error) {
	f.applied = true
	return app.UpdateAllResult{}, nil
}

func TestUpdateDryRunJSONAndHumanPreview(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		service := &updatePlanFakeService{plan: app.UpdatePlan{Schema: 1, Platform: "linux/amd64", RegistryRevision: "registry-revision", Applications: []app.UpdatePlanEntry{{ID: "fixture", Name: "Fixture", Status: "update", Current: app.UpdatePackage{Version: "1.0", Channel: "stable", Fingerprint: "old"}, Target: app.UpdatePackage{Version: "1.0", Channel: "stable", Fingerprint: "new"}, Rollback: app.UpdatePackage{Version: "1.0", Channel: "stable", Fingerprint: "old"}, PackageChanged: true, RuntimeChanged: true}}}}
		var out bytes.Buffer
		args := []string{"update", "--all", "--dry-run"}
		if jsonOutput {
			args = append(args, "--json")
		}
		code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), args)
		if code != 0 || service.applied || len(service.installed) != 0 || service.selection != nil {
			t.Fatalf("preview mutated service: code=%d %+v", code, service)
		}
		if jsonOutput {
			var plan app.UpdatePlan
			decoder := json.NewDecoder(&out)
			if err := decoder.Decode(&plan); err != nil {
				t.Fatal(err)
			}
			if plan.Schema != 1 || len(plan.Applications) != 1 || !plan.Applications[0].PackageChanged {
				t.Fatalf("JSON=%+v", plan)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("extra stdout: %v", err)
			}
		} else {
			for _, want := range []string{"1.0 [stable] -> 1.0 [stable]", "old -> new", "Runtime:", "Rollback retained:"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q: %s", want, out.String())
				}
			}
		}
	}
}

func TestUpdateDryRunFailureAndArgumentValidation(t *testing.T) {
	service := &updatePlanFakeService{plan: app.UpdatePlan{Schema: 1, Applications: []app.UpdatePlanEntry{{ID: "missing", Status: "error", Code: app.CodeNotInstalled, Reason: "missing installation"}}}}
	var out bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: io.Discard}
	if code := runner.Run(context.Background(), []string{"update", "missing", "--dry-run", "--json"}); code == 0 {
		t.Fatal("resolution failure returned success")
	}
	if len(service.selection) != 1 || service.selection[0] != "missing" || !json.Valid(out.Bytes()) {
		t.Fatalf("selection=%v JSON=%s", service.selection, out.String())
	}
	for _, args := range [][]string{{"update", "--all", "--json"}, {"update", "--dry-run"}, {"update", "fixture", "--all", "--dry-run"}} {
		if code := runner.Run(context.Background(), args); code != exitInvalidArguments {
			t.Fatalf("args=%v code=%d", args, code)
		}
	}
}
