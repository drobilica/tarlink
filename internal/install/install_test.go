package install

import (
	"archive/tar"
	"archive/zip"
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

func nestedFixtureArchive(t *testing.T, version string) []byte {
	t.Helper()
	var inner bytes.Buffer
	zw := zip.NewWriter(&inner)
	w, err := zw.Create("fixture-" + version + "/bin/run")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(version)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	h := tar.Header{Name: "payload.zip", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(inner.Len())}
	if err := tw.WriteHeader(&h); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(inner.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func corruptNestedFixtureArchive(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	payload := []byte("not-an-archive")
	h := tar.Header{Name: "payload.zip", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(payload))}
	if err := tw.WriteHeader(&h); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
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
	return server.manifestChannel(version, "stable")
}

func (server artifactServer) manifestChannel(version, channel string) *manifest.Manifest {
	digest := sha256.Sum256(server.data)
	release := manifest.Release{Channel: channel, Version: version, URL: server.server.URL, Verification: manifest.Verification{
		Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.server.URL + "/SHA256SUMS",
	}, Archive: "tar.gz"}
	return &manifest.Manifest{
		Schema: 3, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release:        release,
		ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: channel, Channels: map[string]manifest.ChannelHead{channel: {Current: version}}, Releases: []manifest.Release{release}},
		Application:    manifest.Application{Executables: []manifest.Executable{{Name: "run", Path: "bin/run"}}},
		Desktop:        manifest.Desktop{Enabled: true, Categories: []string{"Utility"}},
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
	installed, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	assertCurrent(t, layout, "fixture", "v1")
	if installed.State.Previous != "" {
		t.Fatalf("previous = %q", installed.State.Previous)
	}

	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	updated, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil)
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

func TestUninstallFailureBeforeCleanupPreservesInstallation(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	manager.fail = func(stage string) error {
		if stage == "before_uninstall" {
			return errors.New("injected uninstall failure")
		}
		return nil
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err == nil {
		t.Fatal("Uninstall() unexpectedly succeeded")
	}
	if _, err := state.LoadForApp(layout, "fixture"); err != nil {
		t.Fatalf("state removed after injected failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture", "v1", "bin", "run")); err != nil {
		t.Fatalf("installed executable removed after injected failure: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Bin, "run")); err != nil {
		t.Fatalf("executable integration removed after injected failure: %v", err)
	}
}

func TestUninstallIntegrationConflictPreservesInstallation(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	content, err := os.ReadFile(desktop)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, append(content, []byte("Comment=changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); !errors.Is(err, integration.ErrConflict) {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "fixture"); err != nil {
		t.Fatalf("state removed after integration conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture")); err != nil {
		t.Fatalf("application root removed after integration conflict: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Bin, "run")); err != nil {
		t.Fatalf("executable integration removed after integration conflict: %v", err)
	}
}

func TestNestedReleaseInstallAndFailedUpdatePreserveCurrent(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, nestedFixtureArchive(t, "nested-v1"))
	item := server.manifest("nested-v1")
	item.Release.NestedArchive = manifest.NestedArchive{Path: "payload.zip", Archive: "zip"}
	item.ReleaseHistory.Releases[0].NestedArchive = item.Release.NestedArchive
	manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
	installed, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("nested install = %v", err)
	}
	if installed.State.Current != "nested-v1" {
		t.Fatalf("current = %q", installed.State.Current)
	}

	bad := newArtifactServer(t, corruptNestedFixtureArchive(t))
	update := bad.manifest("nested-v2")
	update.Release.NestedArchive = manifest.NestedArchive{Path: "payload.zip", Archive: "zip"}
	update.ReleaseHistory.Releases[0].NestedArchive = update.Release.NestedArchive
	manager = New(layout, &download.Client{HTTP: bad.server.Client(), RedirectLimit: 2})
	if _, err := manager.UpdateWithOptions(context.Background(), update, Options{Channel: "stable"}, nil); err == nil {
		t.Fatal("corrupt nested update unexpectedly succeeded")
	}
	state, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if state.Current != "nested-v1" {
		t.Fatalf("current after failed update = %q", state.Current)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, "fixture", "nested-v1")); err != nil {
		t.Fatalf("active version missing after failed update: %v", err)
	}
	if stages, err := filepath.Glob(filepath.Join(layout.Apps, ".staging-fixture-*")); err != nil || len(stages) != 0 {
		t.Fatalf("staging cleanup = %v, err = %v", stages, err)
	}
}

func TestLifecycleTracksChannelPinAndRollback(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifestChannel("v1", "nightly"), Options{Channel: "nightly"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetPinned(context.Background(), "fixture", true); err != nil {
		t.Fatal(err)
	}
	installed, err := state.LoadForApp(layout, "fixture")
	if err != nil || installed.Channel != "nightly" || !installed.Pinned {
		t.Fatalf("initial state = %#v, %v", installed, err)
	}

	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	// An explicit target is allowed to change a pinned installation and keeps
	// the pin bit intact.
	updated, err := manager.UpdateWithOptions(context.Background(), v2Server.manifestChannel("v2", "nightly"), Options{Channel: "nightly", Explicit: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.Channel != "nightly" || !updated.State.Pinned || updated.State.Previous != "v1" {
		t.Fatalf("updated state = %#v", updated.State)
	}
	if _, err := manager.UpdateWithOptions(context.Background(), v1Server.manifestChannel("v1", "nightly"), Options{Channel: "nightly"}, nil); !errors.Is(err, ErrPinned) {
		t.Fatalf("implicit pinned update error = %v, want ErrPinned", err)
	}

	rolledBack, err := manager.Rollback(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State.Current != "v1" || rolledBack.State.Previous != "v2" || rolledBack.State.Channel != "nightly" || rolledBack.State.PreviousChannel != "nightly" || !rolledBack.State.Pinned {
		t.Fatalf("rollback state = %#v", rolledBack.State)
	}
	assertCurrent(t, layout, "fixture", "v1")
	if err := manager.SetPinned(context.Background(), "fixture", false); err != nil {
		t.Fatal(err)
	}
	installed, err = state.LoadForApp(layout, "fixture")
	if err != nil || installed.Pinned {
		t.Fatalf("unpinned state = %#v, %v", installed, err)
	}
}

func TestLifecycleChannelSwitchPreservesPreviousChannel(t *testing.T) {
	layout := testLayout(t)
	stableServer := newArtifactServer(t, fixtureArchive(t, "stable-1"))
	manager := managerFor(t, layout, stableServer)
	if _, err := manager.InstallWithOptions(context.Background(), stableServer.manifest("stable-1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	nightlyServer := newArtifactServer(t, fixtureArchive(t, "nightly-1"))
	manager.Client = &download.Client{HTTP: nightlyServer.server.Client(), RedirectLimit: 2}
	updated, err := manager.UpdateWithOptions(context.Background(), nightlyServer.manifestChannel("nightly-1", "nightly"), Options{Channel: "nightly", Explicit: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.Channel != "nightly" || updated.State.PreviousChannel != "stable" {
		t.Fatalf("channel switch state = %#v", updated.State)
	}
	rolledBack, err := manager.Rollback(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.State.Current != "stable-1" || rolledBack.State.Channel != "stable" || rolledBack.State.PreviousChannel != "nightly" {
		t.Fatalf("switched rollback state = %#v", rolledBack.State)
	}
}

func TestInstallReportsArchiveExtractionProgress(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	var stages []string
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, func(stage string, _, _ int64) {
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
	if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); !errors.Is(err, download.ErrChecksumMismatch) {
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
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
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
	if _, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil); err == nil {
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
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
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
	if outcome, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil); !errors.Is(err, injected) || outcome.State.Current != "v2" {
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
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
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
	if _, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil); !errors.Is(err, filesystem.ErrSymlink) {
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
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	if _, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil); err != nil {
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
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); !errors.Is(err, integration.ErrConflict) {
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
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("conflicting install error = %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
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
			_, err := manager.UpdateWithOptions(context.Background(), v2Server.manifest("v2"), Options{Channel: "stable"}, nil)
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
