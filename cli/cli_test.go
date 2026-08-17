package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

type fakeService struct {
	applications []app.Application
}

func (f *fakeService) Install(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, errors.New("unused")
}
func (f *fakeService) Update(context.Context, string, app.ProgressSink) (app.Result, error) {
	return app.Result{}, errors.New("unused")
}
func (f *fakeService) UpdateAll(context.Context, app.ProgressSink) (app.UpdateAllResult, error) {
	return app.UpdateAllResult{}, errors.New("unused")
}
func (f *fakeService) Remove(context.Context, string, app.ProgressSink) error {
	return errors.New("unused")
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

func TestNoArgumentsShowsHelp(t *testing.T) {
	var out bytes.Buffer
	code := (Runner{Stdout: &out, Stderr: &bytes.Buffer{}}).Run(context.Background(), nil)
	if code != 0 || !bytes.Contains(out.Bytes(), []byte("Usage:")) {
		t.Fatalf("code=%d output=%q", code, out.String())
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

func TestInvalidArgumentsHaveStableExit(t *testing.T) {
	var errOut bytes.Buffer
	code := (Runner{Service: &fakeService{}, Stdout: &bytes.Buffer{}, Stderr: &errOut}).Run(context.Background(), []string{"info"})
	if code != exitInvalidArguments || errOut.Len() == 0 {
		t.Fatalf("code=%d stderr=%q", code, errOut.String())
	}
}
