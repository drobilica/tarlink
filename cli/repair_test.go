package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

type repairFakeService struct {
	fakeService
	repaired  string
	repairErr error
}

func (f *repairFakeService) Repair(_ context.Context, id string, _ app.ProgressSink) (app.Result, error) {
	f.repaired = id
	return app.Result{AppID: id, Version: "1.0"}, f.repairErr
}
func TestRepairCommand(t *testing.T) {
	service := &repairFakeService{}
	var out bytes.Buffer
	runner := Runner{Service: service, Stdout: &out, Stderr: io.Discard}
	if code := runner.Run(context.Background(), []string{"repair", "fixture"}); code != 0 || service.repaired != "fixture" || !strings.Contains(out.String(), "Repaired fixture 1.0") {
		t.Fatalf("repair: code=%d id=%s output=%s", code, service.repaired, out.String())
	}
	service.repairErr = errors.New("repair refused")
	if code := runner.Run(context.Background(), []string{"repair", "fixture"}); code == 0 {
		t.Fatal("repair error returned success")
	}
	for _, args := range [][]string{{"repair"}, {"repair", "one", "two"}} {
		if code := runner.Run(context.Background(), args); code != exitInvalidArguments {
			t.Fatalf("args %v: code=%d", args, code)
		}
	}
}
