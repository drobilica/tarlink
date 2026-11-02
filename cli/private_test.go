package cli

import (
	"bytes"
	"context"
	"github.com/drobilica/tarlink/internal/app"
	"strings"
	"testing"
)

type privateFake struct {
	fakeService
	repositoryID         int64
	operation, selection string
}

func (f *privateFake) EnrollPrivate(_ context.Context, id int64) error {
	f.repositoryID = id
	return nil
}
func (f *privateFake) PrivateOperation(_ context.Context, operation, selection string, _ app.ProgressSink) (app.Result, error) {
	f.operation, f.selection = operation, selection
	return app.Result{AppID: "recit", Version: "v0.1.0"}, nil
}
func TestPrivateCLIExplicitRouting(t *testing.T) {
	f := &privateFake{}
	var out, errs bytes.Buffer
	r := Runner{Service: f, Stdout: &out, Stderr: &errs}
	if code := r.Run(context.Background(), []string{"private", "enroll", "--repository-id", "1442652140"}); code != 0 || f.repositoryID != 1442652140 {
		t.Fatal(code, errs.String())
	}
	for _, operation := range []string{"install", "update", "repair"} {
		if code := r.Run(context.Background(), []string{"private", operation, "recit"}); code != 0 || f.operation != operation || f.selection != "recit" {
			t.Fatal(operation, code, errs.String())
		}
	}
	out.Reset()
	if code := r.Run(context.Background(), []string{"private", "--help"}); code != 0 || !strings.Contains(out.String(), "TARLINK_PRIVATE_GITHUB_TOKEN") {
		t.Fatal(code, out.String())
	}
}
