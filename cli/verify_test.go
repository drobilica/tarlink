package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/integrity"
)

type verifyFakeService struct {
	fakeService
	id     string
	report app.VerifyReport
	err    error
}

func (f *verifyFakeService) Verify(_ context.Context, id string) (app.VerifyReport, error) {
	f.id = id
	return f.report, f.err
}
func TestVerifyCommand(t *testing.T) {
	service := &verifyFakeService{report: app.VerifyReport{Payloads: []app.VerifyPayload{{App: "fixture", Retention: "current", Version: "1", Status: "ok"}}}}
	var out, stderr bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: &stderr}
	if code := runner.Run(context.Background(), []string{"verify", "fixture"}); code != 0 || service.id != "fixture" || !strings.Contains(out.String(), "fixture current 1: ok") {
		t.Fatalf("success: %d %s", code, out.String())
	}
	if code := runner.Run(context.Background(), []string{"verify", "--all"}); code != 0 || service.id != "" {
		t.Fatalf("all: %d %s", code, service.id)
	}
	service.report.Issues = 1
	service.report.Payloads[0].Status = "changed"
	service.report.Payloads[0].Changes = []integrity.Change{{Path: "appimage", Status: "changed"}}
	out.Reset()
	if code := runner.Run(context.Background(), []string{"verify", "fixture"}); code != exitStateCorrupt || !strings.Contains(out.String(), `changed "appimage"`) || !strings.Contains(stderr.String(), "self-updates") {
		t.Fatalf("changed: %d %s %s", code, out.String(), stderr.String())
	}
	service.err = errors.New("failed")
	if code := runner.Run(context.Background(), []string{"verify", "fixture"}); code == 0 {
		t.Fatal("error succeeded")
	}
	for _, args := range [][]string{{"verify"}, {"verify", "one", "two"}, {"verify", "--all", "one"}, {"verify", "--offline", "one"}} {
		if code := runner.Run(context.Background(), args); code != exitInvalidArguments {
			t.Fatalf("args %v: %d", args, code)
		}
	}
	if code := (Runner{Service: &fakeService{}, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"verify", "fixture"}); code == 0 {
		t.Fatal("missing service succeeded")
	}
	service.err = nil
	service.report = app.VerifyReport{}
	out.Reset()
	if code := runner.Run(context.Background(), []string{"verify", "--all"}); code != 0 || !strings.Contains(out.String(), "No installed applications") {
		t.Fatalf("empty: %d %s", code, out.String())
	}
}
