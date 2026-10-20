package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
	"github.com/drobilica/tarlink/internal/artifactrepo"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

type offlineFakeService struct {
	fakeService
	enabled  bool
	repaired string
}

func (f *offlineFakeService) EnableOffline() { f.enabled = true }
func (f *offlineFakeService) Repair(_ context.Context, id string, _ app.ProgressSink) (app.Result, error) {
	f.repaired = id
	return app.Result{AppID: id, Version: "1.0"}, nil
}

func TestOfflineFlagConfiguresInstallAndRepairBeforeServiceCalls(t *testing.T) {
	for _, args := range [][]string{{"--offline", "install", "fixture"}, {"install", "fixture", "--offline"}, {"--offline", "repair", "fixture"}, {"repair", "fixture", "--offline"}} {
		service := &offlineFakeService{}
		var out bytes.Buffer
		if code := (Runner{Service: service, Stdout: &out, Stderr: io.Discard}).Run(context.Background(), args); code != 0 || !service.enabled {
			t.Fatalf("args=%v code=%d offline=%v", args, code, service.enabled)
		}
		if len(service.installed) == 0 && service.repaired == "" {
			t.Fatalf("no requested operation: %v", args)
		}
	}
}

func TestOfflineUnsupportedCommandsCannotStartServicesOrTUI(t *testing.T) {
	for _, args := range [][]string{{"--offline"}, {"--offline", "refresh"}, {"self-update", "--offline"}, {"--offline", "update", "--all"}, {"--offline", "registry", "inspect", "owner/repository"}, {"registry", "candidates", "discover", "--catalog", "quiver:/catalog", "--registry", "/registry", "--offline"}, {"--offline", "repository", "sync", "/repository"}} {
		service := &offlineFakeService{}
		launched := false
		runner := Runner{Service: service, Stdout: io.Discard, Stderr: io.Discard, LaunchTUI: func(context.Context, app.Service, io.Writer, io.Writer) error { launched = true; return nil }}
		if code := runner.Run(context.Background(), args); code != exitInvalidArguments {
			t.Fatalf("args=%v code=%d", args, code)
		}
		if service.enabled || service.synced || launched {
			t.Fatalf("unsupported offline command reached service: %v", args)
		}
	}
}

func TestOfflineHelpAndExplicitFalseHaveNoOfflineSideEffects(t *testing.T) {
	var help bytes.Buffer
	if code := (Runner{Stdout: &help, Stderr: io.Discard}).Run(context.Background(), []string{"--offline", "install", "--help"}); code != 0 {
		t.Fatalf("offline help=%d", code)
	}
	service := &offlineFakeService{}
	if code := (Runner{Service: service, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"install", "fixture", "--offline=false"}); code != 0 || service.enabled {
		t.Fatalf("false flag enabled offline: code=%d enabled=%v", code, service.enabled)
	}
	if code := (Runner{Service: &fakeService{}, Stdout: io.Discard, Stderr: io.Discard}).Run(context.Background(), []string{"install", "fixture", "--offline"}); code == 0 {
		t.Fatal("service lacking offline support accepted flag")
	}
}

func TestOfflineCLIWithRealCoreInstallsAndRepairsFromFilesystemRepository(t *testing.T) {
	ctx := context.Background()
	var payload bytes.Buffer
	gz := gzip.NewWriter(&payload)
	tw := tar.NewWriter(gz)
	executable := []byte("verified CLI fixture")
	if err := tw.WriteHeader(&tar.Header{Name: "fixture", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(executable))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload.Bytes()))
	manifest := `schema: 5
id: fixture
name: Fixture
summary: Offline CLI fixture
homepage: https://example.com/
categories: [utilities]
release:
  current: "1.0"
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: "1.0"
      artifacts:
        linux-` + runtime.GOARCH + `:
          url: https://example.com/fixture.tar.gz
          verification:
            digest: ` + digest + `
            source: https://example.com/releases
application:
  executable:
    name: fixture
    path: fixture
`
	var registryArchive bytes.Buffer
	gz = gzip.NewWriter(&registryArchive)
	tw = tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "registry-main/"}, {name: "registry-main/apps/"}, {name: "registry-main/apps/fixture/"}, {name: "registry-main/apps/fixture/manifest.yaml", body: []byte(manifest)},
	} {
		header := tar.Header{Name: entry.name, Typeflag: tar.TypeDir, Mode: 0o755}
		if entry.body != nil {
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.body))
			header.Mode = 0o644
		}
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	deny := false
	client := download.NewClient()
	client.HTTP.Transport = offlineCLIRoundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		if deny {
			return nil, errors.New("unexpected HTTP after offline selection")
		}
		return &http.Response{StatusCode: 200, Request: request, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(registryArchive.Bytes())), ContentLength: int64(registryArchive.Len())}, nil
	})
	core, err := app.NewCore(layout, client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := core.SyncRegistry(ctx, nil); err != nil {
		t.Fatal(err)
	}
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}
	if err := artifactrepo.Add(ctx, repository, "sha256", digest, func(_ context.Context, _, _, _, path string) error { return os.WriteFile(path, payload.Bytes(), 0o600) }, "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := core.AddRepositorySource("https://mirror.invalid/content"); err != nil {
		t.Fatal(err)
	}
	if err := core.AddRepositorySource(repository); err != nil {
		t.Fatal(err)
	}
	deny = true
	before := requests
	var out, errOut bytes.Buffer
	runner := Runner{Service: core, Stdout: &out, Stderr: &errOut}
	if code := runner.Run(ctx, []string{"--offline", "install", "fixture", "--force-path"}); code != 0 {
		t.Fatalf("offline install code=%d stderr=%s", code, errOut.String())
	}
	path := filepath.Join(layout.Apps, "fixture", "current", "fixture")
	if err := os.WriteFile(path, []byte("damaged"), 0o700); err != nil {
		t.Fatal(err)
	}
	if code := runner.Run(ctx, []string{"--offline", "repair", "fixture"}); code != 0 {
		t.Fatalf("offline repair code=%d stderr=%s", code, errOut.String())
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != string(executable) || requests != before {
		t.Fatalf("payload=%q err=%v requests=%d want=%d", body, err, requests, before)
	}
}

type offlineCLIRoundTrip func(*http.Request) (*http.Response, error)

func (f offlineCLIRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
