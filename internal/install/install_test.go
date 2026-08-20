package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/state"
)

func fixtureArchive(t *testing.T, version string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "fixture-" + version + "/"
	for _, header := range []tar.Header{
		{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/run", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(version))},
	} {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "run") {
			if _, err := tarWriter.Write([]byte(version)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testLayout(t *testing.T) filesystem.Layout {
	t.Helper()
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	return layout
}

type artifactServer struct {
	server *httptest.Server
	data   []byte
}

func newArtifactServer(t *testing.T, data []byte) artifactServer {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(data)
	}))
	t.Cleanup(server.Close)
	return artifactServer{server: server, data: data}
}

func (server artifactServer) manifest(version string) *manifest.Manifest {
	digest := sha256.Sum256(server.data)
	return &manifest.Manifest{
		Schema: 1, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: manifest.Release{Version: version, URL: server.server.URL, Verification: manifest.Verification{
			Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.server.URL + "/SHA256SUMS",
		}, Archive: "tar.gz"},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: "run", Path: "bin/run"}}},
		Desktop:     manifest.Desktop{Enabled: true, Categories: []string{"Utility"}},
	}
}

func managerFor(t *testing.T, layout filesystem.Layout, server artifactServer) *Manager {
	t.Helper()
	return New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
}

func TestLifecycleInstallUpdateRollbackUninstall(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	installed, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	assertCurrent(t, layout, "fixture", "v1")
	if installed.State.Previous != "" {
		t.Fatalf("previous = %q", installed.State.Previous)
	}

	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	updated, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.State.Previous != "v1" {
		t.Fatalf("previous = %q", updated.State.Previous)
	}
	assertCurrent(t, layout, "fixture", "v2")
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture", "v1")); err != nil {
		t.Fatalf("previous version missing: %v", err)
	}

	rolledBack, err := manager.Rollback(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if rolledBack.State.Previous != "v2" {
		t.Fatalf("previous after rollback = %q", rolledBack.State.Previous)
	}
	assertCurrent(t, layout, "fixture", "v1")

	unrelated := filepath.Join(layout.Home, "project.blend")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
		t.Fatalf("application root remains: %v", err)
	}
	if content, err := os.ReadFile(unrelated); err != nil || string(content) != "keep" {
		t.Fatalf("unrelated file changed: %q, %v", content, err)
	}
}

func TestInstallReportsArchiveExtractionProgress(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	var stages []string
	if _, err := manager.Install(context.Background(), server.manifest("v1"), func(stage string, _, _ int64) {
		stages = append(stages, stage)
	}); err != nil {
		t.Fatal(err)
	}
	preparing, extracting := false, false
	for _, stage := range stages {
		preparing = preparing || stage == "extracting-preparing"
		extracting = extracting || stage == "extracting"
	}
	if !extracting || preparing {
		t.Fatalf("archive progress stages = %v", stages)
	}
}

func TestInstallVerificationFailurePrecedesExtraction(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	item := server.manifest("v1")
	item.Release.Verification.Digest = strings.Repeat("0", 64)
	manager := managerFor(t, layout, server)
	if _, err := manager.Install(context.Background(), item, nil); !errors.Is(err, download.ErrChecksumMismatch) {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
		t.Fatalf("application root exists after verification failure: %v", err)
	}
	if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
		t.Fatalf("state exists after verification failure: %v", err)
	}
}

func TestUpdateStateFailureRestoresCurrentAndCleansNewVersion(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); err != nil {
		t.Fatal(err)
	}
	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	manager.fail = func(stage string) error {
		if stage == "before_state" {
			return errors.New("injected state failure")
		}
		return nil
	}
	if _, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil); err == nil {
		t.Fatal("Update() unexpectedly succeeded")
	}
	assertCurrent(t, layout, "fixture", "v1")
	installed, err := state.LoadForApp(layout, "fixture")
	if err != nil || installed.Current != "v1" || installed.Previous != "" {
		t.Fatalf("state = %#v, %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture", "v2")); !os.IsNotExist(err) {
		t.Fatalf("failed version remains: %v", err)
	}
}

func TestUpdatePostCommitStateSyncFailureKeepsConsistentVersion(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); err != nil {
		t.Fatal(err)
	}
	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	injected := errors.New("injected post-rename state sync failure")
	manager.writeState = func(layout filesystem.Layout, installed state.State) (bool, error) {
		committed, err := state.WriteForAppWithCommit(layout, installed)
		if err != nil {
			return committed, err
		}
		return true, injected
	}
	if outcome, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil); !errors.Is(err, injected) || outcome.State.Current != "v2" {
		t.Fatalf("Update() outcome=%#v error=%v", outcome, err)
	}
	assertCurrent(t, layout, "fixture", "v2")
	installed, err := state.LoadForApp(layout, "fixture")
	if err != nil || installed.Current != "v2" || installed.Previous != "v1" {
		t.Fatalf("state = %#v, %v", installed, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture", "v2")); err != nil {
		t.Fatalf("committed version missing: %v", err)
	}
}

func TestUpdateRejectsSymlinkedApplicationRoot(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(layout.Apps, "fixture")
	if err := os.Rename(appRoot, appRoot+"-saved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("user owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, appRoot); err != nil {
		t.Fatal(err)
	}
	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	if _, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil); !errors.Is(err, filesystem.ErrSymlink) {
		t.Fatalf("Update() error = %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "user owned" {
		t.Fatalf("outside sentinel changed: %q, %v", content, err)
	}
}

func TestRollbackRejectsSymlinkedAppsRoot(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); err != nil {
		t.Fatal(err)
	}
	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	if _, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(layout.Apps, layout.Apps+"-saved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	if err := os.WriteFile(sentinel, []byte("user owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, layout.Apps); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rollback(context.Background(), "fixture", nil); !errors.Is(err, filesystem.ErrSymlink) {
		t.Fatalf("Rollback() error = %v", err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "user owned" {
		t.Fatalf("outside sentinel changed: %q, %v", content, err)
	}
}

func TestInstallRefusesIntegrationConflict(t *testing.T) {
	layout := testLayout(t)
	conflict := filepath.Join(layout.Bin, "run")
	if err := os.WriteFile(conflict, []byte("user owned"), 0o700); err != nil {
		t.Fatal(err)
	}
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.Install(context.Background(), server.manifest("v1"), nil); !errors.Is(err, integration.ErrConflict) {
		t.Fatalf("Install() error = %v", err)
	}
	content, _ := os.ReadFile(conflict)
	if string(content) != "user owned" {
		t.Fatalf("conflict overwritten: %q", content)
	}
	if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
		t.Fatalf("state unexpectedly exists: %v", err)
	}
}

func TestMutationsReportPerApplicationLockConflict(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	manager.LockTimeout = 20 * time.Millisecond

	held, err := locking.AcquireApp(context.Background(), layout.Locks, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("conflicting install error = %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(context.Background(), v1Server.manifest("v1"), nil); err != nil {
		t.Fatal(err)
	}

	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	held, err = locking.AcquireApp(context.Background(), layout.Locks, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	checks := []struct {
		name string
		run  func() error
	}{
		{"update", func() error {
			_, err := manager.Update(context.Background(), v2Server.manifest("v2"), nil)
			return err
		}},
		{"uninstall", func() error { return manager.Uninstall(context.Background(), "fixture", nil) }},
		{"rollback", func() error {
			_, err := manager.Rollback(context.Background(), "fixture", nil)
			return err
		}},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, locking.ErrConflict) {
				t.Fatalf("conflicting %s error = %v", check.name, err)
			}
		})
	}
	assertCurrent(t, layout, "fixture", "v1")
}

func TestMutationsReportLifecycleLockConflict(t *testing.T) {
	layout := testLayout(t)
	manager := New(layout, nil)
	manager.LockTimeout = 20 * time.Millisecond
	held, err := locking.AcquireDirectoryWithTimeout(context.Background(), layout.Home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	if _, err := manager.Rollback(context.Background(), "fixture", nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("lifecycle conflict error = %v", err)
	}
}

func assertCurrent(t *testing.T, layout filesystem.Layout, appID, version string) {
	t.Helper()
	target, err := os.Readlink(filepath.Join(layout.Apps, appID, "current"))
	if err != nil || target != version {
		t.Fatalf("current = %q, %v; want %q", target, err, version)
	}
}
