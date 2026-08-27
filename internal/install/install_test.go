package install

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
		Schema: manifest.SchemaV5, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
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

// multiRouteServer serves distinct bytes per URL path and 404s everything
// else, so artifact and icon downloads are independent.
type multiRouteServer struct {
	server *httptest.Server
}

func newMultiRouteServer(t *testing.T, routes map[string][]byte) multiRouteServer {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, ok := routes[request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(body)
	}))
	t.Cleanup(server.Close)
	return multiRouteServer{server: server}
}

func (server multiRouteServer) manifest(t *testing.T, version string, routes map[string][]byte, artifactPath, iconPath string) *manifest.Manifest {
	t.Helper()
	artifact := routes[artifactPath]
	digest := sha256.Sum256(artifact)
	release := manifest.Release{Channel: "stable", Version: version, URL: server.server.URL + artifactPath, Verification: manifest.Verification{
		Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.server.URL + "/SHA256SUMS",
	}, Archive: "tar.gz"}
	iconDigest := sha256.Sum256(routes[iconPath])
	return &manifest.Manifest{
		Schema: manifest.SchemaV5, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release:        release,
		ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: version}}, Releases: []manifest.Release{release}},
		Application:    manifest.Application{Executables: []manifest.Executable{{Name: "run", Path: "bin/run"}}},
		Desktop: manifest.Desktop{Enabled: true, Categories: []string{"Utility"}, Icon: manifest.DesktopIcon{
			URL: server.server.URL + iconPath, SHA256: hex.EncodeToString(iconDigest[:]),
		}},
	}
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

func TestSameVersionFingerprintSeparatesMaterialChangesButReconcilesMetadata(t *testing.T) {
	layout := testLayout(t)
	first := newArtifactServer(t, fixtureArchive(t, "same-version-a"))
	manager := managerFor(t, layout, first)
	initial := first.manifest("1.0")
	installed, err := manager.InstallWithOptions(context.Background(), initial, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstPath, err := layout.PackagePath("fixture", installed.State.Current, installed.State.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	beforeState := installed.State
	beforePayload, err := os.ReadFile(filepath.Join(firstPath, "bin", "run"))
	if err != nil {
		t.Fatal(err)
	}

	metadata := first.manifest("1.0")
	metadata.Summary = "metadata changed"
	if _, err := manager.UpdateWithOptions(context.Background(), metadata, Options{Channel: "stable"}, nil); !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("metadata-only update error = %v, want ErrNoUpdate", err)
	}
	afterState, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatalf("load state after metadata-only update: %v", err)
	}
	if !reflect.DeepEqual(afterState, beforeState) {
		t.Fatalf("metadata-only update changed state: before=%#v after=%#v", beforeState, afterState)
	}
	afterPayload, err := os.ReadFile(filepath.Join(firstPath, "bin", "run"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterPayload, beforePayload) {
		t.Fatalf("metadata-only update changed payload: before=%q after=%q", beforePayload, afterPayload)
	}

	second := newArtifactServer(t, fixtureArchive(t, "same-version-b"))
	manager.Client = &download.Client{HTTP: second.server.Client(), RedirectLimit: 2}
	material := second.manifest("1.0")
	updated, err := manager.UpdateWithOptions(context.Background(), material, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("material same-version update: %v", err)
	}
	secondPath, err := layout.PackagePath("fixture", updated.State.Current, updated.State.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if secondPath == firstPath || updated.State.Previous != "1.0" || updated.State.PreviousFingerprint == updated.State.CurrentFingerprint {
		t.Fatalf("material change did not create distinct retained package: state=%#v", updated.State)
	}
	if _, err := os.Stat(firstPath); err != nil {
		t.Fatalf("previous package missing after same-version material update: %v", err)
	}
}

func TestUpdateReconcilesManagedBinLinkToDesktopOnly(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	initial := server.manifest("v1")
	if _, err := manager.InstallWithOptions(context.Background(), initial, Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(layout.Bin, "run")
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("managed bin link after install: %v", err)
	}

	desktopOnly := server.manifest("v1")
	createBinLink := false
	desktopOnly.Application.Executables[0].CreateBinLink = &createBinLink
	desktopOnly.Desktop.Executable = "run"
	desktopOnly.Desktop.WorkingDirectory = "application-root"
	updated, err := manager.UpdateWithOptions(context.Background(), desktopOnly, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("stale managed bin link remains after reconciliation: %v", err)
	}
	if updated.State.Integration.Executables[0].CreateBinLink == nil || *updated.State.Integration.Executables[0].CreateBinLink {
		t.Fatalf("reconciled state bin-link decision = %#v", updated.State.Integration.Executables[0].CreateBinLink)
	}
	desktop, err := os.ReadFile(filepath.Join(layout.Desktop, "tarlink-fixture.desktop"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(desktop)
	if !strings.Contains(content, "Exec="+filepath.Join(layout.Apps, "fixture", "current", "bin", "run")) || !strings.Contains(content, "Path="+filepath.Join(layout.Apps, "fixture", "current")) {
		t.Fatalf("desktop-only launch configuration = %q", content)
	}

	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateTogglesDesktopIntegrationAndRollsBackBeforeState(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	if _, err := os.Stat(desktop); err != nil {
		t.Fatalf("desktop entry after install: %v", err)
	}

	v2Server := newArtifactServer(t, fixtureArchive(t, "v2"))
	v2 := v2Server.manifest("v2")
	v2.Desktop.Enabled = false
	v2.Desktop.Categories = nil
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	manager.fail = func(stage string) error {
		if stage == "before_state" {
			return errors.New("injected state failure")
		}
		return nil
	}
	if _, err := manager.UpdateWithOptions(context.Background(), v2, Options{Channel: "stable"}, nil); err == nil {
		t.Fatal("disabled update unexpectedly succeeded with injected state failure")
	}
	manager.fail = nil
	current, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Validate(); err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
	if !current.DesktopEnabled {
		t.Fatal("desktop integration disabled after failed update")
	}
	if _, err := os.Stat(desktop); err != nil {
		t.Fatalf("desktop entry not restored after failed update: %v", err)
	}

	updated, err := manager.UpdateWithOptions(context.Background(), v2, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.DesktopEnabled {
		t.Fatal("desktop integration remains enabled after disabled update")
	}
	if err := updated.State.Validate(); err != nil {
		t.Fatalf("disabled state: %v", err)
	}
	if _, err := os.Lstat(desktop); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains after disabled update: %v", err)
	}

	v3Server := newArtifactServer(t, fixtureArchive(t, "v3"))
	v3 := v3Server.manifest("v3")
	manager.Client = &download.Client{HTTP: v3Server.server.Client(), RedirectLimit: 2}
	enabled, err := manager.UpdateWithOptions(context.Background(), v3, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.State.DesktopEnabled {
		t.Fatal("desktop integration remains disabled after enabled update")
	}
	if err := enabled.State.Validate(); err != nil {
		t.Fatalf("enabled state: %v", err)
	}
	if _, err := os.Stat(desktop); err != nil {
		t.Fatalf("desktop entry after enabled update: %v", err)
	}

	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(desktop); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains after uninstall: %v", err)
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
	installed, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	packagePath, err := layout.PackagePath("fixture", installed.Current, installed.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(packagePath, "bin", "run")); err != nil {
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

func TestUninstallConflictRecoveryRemovesModifiedDesktopAndCompletes(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	if err := os.WriteFile(desktop, []byte("user-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := manager.Uninstall(context.Background(), "fixture", nil)
	var conflict *UninstallConflictError
	if !errors.As(err, &conflict) || conflict.Path != desktop {
		t.Fatalf("Uninstall() error = %v, conflict = %#v", err, conflict)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatalf("retry uninstall: %v", err)
	}
	if _, err := os.Lstat(desktop); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("desktop entry remains: %v", err)
	}
}

func TestUninstallConflictRecoveryCancellationAndArbitraryPathAreSafe(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	original, err := os.ReadFile(desktop)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", filepath.Join(layout.Desktop, "other.desktop")); !errors.Is(err, integration.ErrConflict) {
		t.Fatalf("arbitrary path error = %v", err)
	}
	if got, _ := os.ReadFile(desktop); string(got) != "changed\n" {
		t.Fatalf("conflict file changed after rejected path: %q", got)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(desktop); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("confirmed conflict remains: %v", err)
	}
	if string(original) == "changed\n" {
		t.Fatal("fixture did not create a modified ownership conflict")
	}
	if _, err := state.LoadForApp(layout, "fixture"); err != nil {
		t.Fatalf("installation was changed before uninstall retry: %v", err)
	}
}

func TestUninstallConflictRecoveryRemovesSymlinkOnlyAndHandlesMultipleConflicts(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	if err := os.Remove(desktop); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "untouched")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, desktop); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(layout.Bin, "run")
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := manager.Uninstall(context.Background(), "fixture", nil)
	var first *UninstallConflictError
	if !errors.As(err, &first) || first.Path != bin {
		t.Fatalf("first conflict = %v, typed = %#v", err, first)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", bin); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err == nil {
		t.Fatal("uninstall unexpectedly skipped remaining conflict")
	} else {
		var next *UninstallConflictError
		if !errors.As(err, &next) || next.Path != desktop {
			t.Fatalf("second conflict = %v, typed = %#v", err, next)
		}
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatalf("symlink target was changed: %q", got)
	}
}

func TestUninstallConflictRecoveryIsIdempotentWhenEntryDisappears(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	desktop := filepath.Join(layout.Desktop, "tarlink-fixture.desktop")
	if err := os.WriteFile(desktop, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err == nil {
		t.Fatal("uninstall unexpectedly succeeded")
	}
	if err := os.Remove(desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
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
	installed, err := state.LoadForApp(layout, appID)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	target, err := os.Readlink(filepath.Join(layout.Apps, appID, "current"))
	packagePath, pathErr := layout.PackagePath(appID, version, installed.CurrentFingerprint)
	expected, relErr := filepath.Rel(filepath.Join(layout.Apps, appID), packagePath)
	if err != nil || pathErr != nil || relErr != nil || target != expected {
		t.Fatalf("current = %q, %v; want %q", target, err, expected)
	}
}

func minimalPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	data := make([]byte, 29)
	copy(data[0:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	binary.BigEndian.PutUint32(data[8:12], 13)
	copy(data[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(data[16:20], uint32(width))
	binary.BigEndian.PutUint32(data[20:24], uint32(height))
	data[24], data[25], data[26], data[27], data[28] = 8, 2, 0, 0, 0
	return data
}

func TestRemoteIconInstallRetainsBytesAndPlacesThemedIcon(t *testing.T) {
	layout := testLayout(t)
	icon := minimalPNG(t, 512, 512)
	routes := map[string][]byte{
		"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
		"/icons/hicolor/512x512/apps/fixture.png": icon,
	}
	server := newMultiRouteServer(t, routes)
	manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
	item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
	installed, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	retainedPath, err := layout.PackagePath("fixture", installed.State.Current, installed.State.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(retainedPath, ".tarlink-icon.png")
	if content, err := os.ReadFile(retained); err != nil || string(content) != string(icon) {
		t.Fatalf("retained icon = %q, %v", content, err)
	}
	themed := filepath.Join(layout.Icons, "512x512", "apps", "tarlink-fixture.png")
	if content, err := os.ReadFile(themed); err != nil || string(content) != string(icon) {
		t.Fatalf("themed icon = %q, %v", content, err)
	}
	st, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if st.Integration.IconSize != 512 || st.Integration.IconSource != ".tarlink-icon.png" {
		t.Fatalf("icon state = %#v", st.Integration)
	}
	if err := st.ValidateForLayout(layout); err != nil {
		t.Fatalf("state after remote icon install: %v", err)
	}
	if installed.State.Current != "v1" {
		t.Fatalf("current = %q", installed.State.Current)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(themed); !os.IsNotExist(err) {
		t.Fatalf("themed icon remains after uninstall: %v", err)
	}
}

func TestRemoteIconUpdateRollbackNeedsNoNetwork(t *testing.T) {
	layout := testLayout(t)
	iconV1 := minimalPNG(t, 512, 512)
	iconV2 := minimalPNG(t, 256, 256)
	routes := map[string][]byte{
		"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
		"/icons/hicolor/512x512/apps/fixture.png": iconV1,
		"/fixture-v2.tar.gz":                      fixtureArchive(t, "v2"),
		"/icons/hicolor/256x256/apps/fixture.png": iconV2,
	}
	server := newMultiRouteServer(t, routes)
	client := &download.Client{HTTP: server.server.Client(), RedirectLimit: 2}
	manager := New(layout, client)
	v1 := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
	if _, err := manager.InstallWithOptions(context.Background(), v1, Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	v2 := server.manifest(t, "v2", routes, "/fixture-v2.tar.gz", "/icons/hicolor/256x256/apps/fixture.png")
	if _, err := manager.UpdateWithOptions(context.Background(), v2, Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(layout.Icons, "256x256", "apps", "tarlink-fixture.png")); err != nil {
		t.Fatalf("updated themed icon missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Icons, "512x512", "apps", "tarlink-fixture.png")); !os.IsNotExist(err) {
		t.Fatalf("previous themed icon remains: %v", err)
	}
	// Rollback must restore the previous themed icon from the retained bytes
	// inside the previous version payload; the icon host is gone.
	server.server.Close()
	if _, err := manager.Rollback(context.Background(), "fixture", nil); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	assertCurrent(t, layout, "fixture", "v1")
	if content, err := os.ReadFile(filepath.Join(layout.Icons, "512x512", "apps", "tarlink-fixture.png")); err != nil || string(content) != string(iconV1) {
		t.Fatalf("restored icon = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(layout.Icons, "256x256", "apps", "tarlink-fixture.png")); !os.IsNotExist(err) {
		t.Fatalf("rolled-back 256 icon remains: %v", err)
	}
	st, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if st.Integration.IconSize != 512 || st.Integration.PreviousIconSize != 256 {
		t.Fatalf("rollback icon sizes = %d/%d", st.Integration.IconSize, st.Integration.PreviousIconSize)
	}
	if err := st.ValidateForLayout(layout); err != nil {
		t.Fatalf("state after rollback: %v", err)
	}
}

func TestRemoteIconDownloadFailuresLeaveNoInstallation(t *testing.T) {
	t.Run("checksum mismatch", func(t *testing.T) {
		layout := testLayout(t)
		icon := []byte("icon bytes")
		routes := map[string][]byte{
			"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
			"/icons/hicolor/512x512/apps/fixture.png": icon,
		}
		server := newMultiRouteServer(t, routes)
		item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
		item.Desktop.Icon.SHA256 = strings.Repeat("0", 64)
		manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
		if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); !errors.Is(err, download.ErrChecksumMismatch) {
			t.Fatalf("Install() error = %v", err)
		}
		if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
			t.Fatalf("state exists after icon checksum failure: %v", err)
		}
		if _, err := os.Stat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
			t.Fatalf("app root exists after icon checksum failure: %v", err)
		}
	})
	t.Run("exceeds size limit", func(t *testing.T) {
		layout := testLayout(t)
		icon := bytes.Repeat([]byte("x"), maxRemoteIconBytes+1)
		routes := map[string][]byte{
			"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
			"/icons/hicolor/512x512/apps/fixture.png": icon,
		}
		server := newMultiRouteServer(t, routes)
		item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
		manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
		if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); !errors.Is(err, download.ErrTooLarge) {
			t.Fatalf("Install() error = %v", err)
		}
		if stages, globErr := filepath.Glob(filepath.Join(layout.Apps, ".staging-fixture-*")); globErr != nil || len(stages) != 0 {
			t.Fatalf("staging cleanup = %v, %v", stages, globErr)
		}
	})
	t.Run("missing route", func(t *testing.T) {
		layout := testLayout(t)
		routes := map[string][]byte{"/fixture-v1.tar.gz": fixtureArchive(t, "v1")}
		server := newMultiRouteServer(t, routes)
		item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
		manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
		if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); !errors.Is(err, download.ErrNetwork) {
			t.Fatalf("Install() error = %v", err)
		}
	})
	for name, icon := range map[string][]byte{
		"non-PNG content":        []byte("these are not PNG bytes"),
		"unsupported dimensions": minimalPNG(t, 100, 100),
		"non-square":             minimalPNG(t, 512, 256),
		"truncated PNG":          minimalPNG(t, 512, 512)[:16],
	} {
		t.Run(name, func(t *testing.T) {
			layout := testLayout(t)
			routes := map[string][]byte{
				"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
				"/icons/hicolor/512x512/apps/fixture.png": icon,
			}
			server := newMultiRouteServer(t, routes)
			item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
			manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
			if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); err == nil {
				t.Fatalf("Install() unexpectedly succeeded with invalid PNG")
			}
			if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
				t.Fatalf("state exists after invalid PNG failure: %v", err)
			}
			if _, err := os.Stat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
				t.Fatalf("app root exists after invalid PNG failure: %v", err)
			}
			if stages, globErr := filepath.Glob(filepath.Join(layout.Apps, ".staging-fixture-*")); globErr != nil || len(stages) != 0 {
				t.Fatalf("staging cleanup = %v, %v", stages, globErr)
			}
		})
	}
}

func fixtureArchiveWithIcon(t *testing.T, version string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "fixture-" + version + "/"
	for _, header := range []tar.Header{
		{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/run", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(version))},
		{Name: prefix + "share/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "share/icon.png", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(version + "-icon"))},
	} {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasSuffix(header.Name, "run"):
			if _, err := tarWriter.Write([]byte(version)); err != nil {
				t.Fatal(err)
			}
		case strings.HasSuffix(header.Name, "icon.png"):
			if _, err := tarWriter.Write([]byte(version + "-icon")); err != nil {
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

func (server artifactServer) manifestWithArchiveIcon(version string) *manifest.Manifest {
	item := server.manifest(version)
	item.Desktop.Icon = manifest.DesktopIcon{Path: "share/icon.png"}
	return item
}

func TestArchiveIconInstallUpdateAndRollback(t *testing.T) {
	layout := testLayout(t)
	v1Server := newArtifactServer(t, fixtureArchiveWithIcon(t, "v1"))
	manager := managerFor(t, layout, v1Server)
	if _, err := manager.InstallWithOptions(context.Background(), v1Server.manifestWithArchiveIcon("v1"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	iconV1 := filepath.Join(layout.Icons, "48x48", "apps", "tarlink-fixture.png")
	if content, err := os.ReadFile(iconV1); err != nil || string(content) != "v1-icon" {
		t.Fatalf("v1 icon = %q, %v", content, err)
	}
	v2Server := newArtifactServer(t, fixtureArchiveWithIcon(t, "v2"))
	manager.Client = &download.Client{HTTP: v2Server.server.Client(), RedirectLimit: 2}
	if _, err := manager.UpdateWithOptions(context.Background(), v2Server.manifestWithArchiveIcon("v2"), Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(iconV1); err != nil || string(content) != "v2-icon" {
		t.Fatalf("v2 icon = %q, %v", content, err)
	}
	// Rollback restores the previous version's archive icon from its retained
	// payload without any network access.
	v2Server.server.Close()
	if _, err := manager.Rollback(context.Background(), "fixture", nil); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	assertCurrent(t, layout, "fixture", "v1")
	if content, err := os.ReadFile(iconV1); err != nil || string(content) != "v1-icon" {
		t.Fatalf("rolled-back icon = %q, %v", content, err)
	}
}

func minimalAppImage(t *testing.T) []byte {
	t.Helper()
	data := make([]byte, 64)
	copy(data[0:4], []byte{0x7f, 'E', 'L', 'F'})
	data[4], data[5], data[6] = 2, 1, 1
	data[8], data[9], data[10] = 'A', 'I', 2
	binary.LittleEndian.PutUint16(data[16:18], 3)
	binary.LittleEndian.PutUint16(data[18:20], 0x3e)
	return data
}

func appImageManifest(t *testing.T, server *httptest.Server, version, iconURL, iconDigest string) *manifest.Manifest {
	t.Helper()
	release := manifest.Release{Channel: "stable", Version: version, URL: server.URL + "/fixture.AppImage", Verification: manifest.Verification{
		Algorithm: "sha256", Digest: strings.Repeat("a", 64), Source: server.URL + "/SHA256SUMS",
	}, Archive: "appimage"}
	return &manifest.Manifest{
		Schema: manifest.SchemaV5, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release:        release,
		ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: version}}, Releases: []manifest.Release{release}},
		Application:    manifest.Application{Executables: []manifest.Executable{{Name: "fixture", Path: "appimage"}}},
		Desktop:        manifest.Desktop{Enabled: true, Categories: []string{"Utility"}, Icon: manifest.DesktopIcon{URL: iconURL, SHA256: iconDigest}},
	}
}

func TestAppImageRemoteIconInstallsOpaquePayloadAndIcon(t *testing.T) {
	layout := testLayout(t)
	appImage := minimalAppImage(t)
	icon := minimalPNG(t, 512, 512)
	routes := map[string][]byte{
		"/fixture.AppImage": appImage,
		"/AppIconLarge.png": icon,
	}
	server := newMultiRouteServer(t, routes)
	appDigest := sha256.Sum256(appImage)
	release := appImageManifest(t, server.server, "1.0", server.server.URL+"/AppIconLarge.png", "")
	release.Release.Verification.Digest = hex.EncodeToString(appDigest[:])
	release.ReleaseHistory.Releases[0].Verification.Digest = hex.EncodeToString(appDigest[:])
	iconDigest := sha256.Sum256(icon)
	release.Desktop.Icon.SHA256 = hex.EncodeToString(iconDigest[:])
	manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
	installed, err := manager.InstallWithOptions(context.Background(), release, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	payloadPath, err := layout.PackagePath("fixture", installed.State.Current, installed.State.CurrentFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(payloadPath, "appimage")
	if content, err := os.ReadFile(payload); err != nil || !bytes.Equal(content, appImage) {
		t.Fatalf("opaque AppImage payload changed: %v", err)
	}
	retained := filepath.Join(payloadPath, ".tarlink-icon.png")
	if content, err := os.ReadFile(retained); err != nil || !bytes.Equal(content, icon) {
		t.Fatalf("retained icon = %q, %v", content, err)
	}
	themed := filepath.Join(layout.Icons, "512x512", "apps", "tarlink-fixture.png")
	if content, err := os.ReadFile(themed); err != nil || !bytes.Equal(content, icon) {
		t.Fatalf("themed icon = %q, %v", content, err)
	}
	st, err := state.LoadForApp(layout, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if st.Integration.IconSize != 512 || st.Integration.IconSource != ".tarlink-icon.png" {
		t.Fatalf("icon state = %#v", st.Integration)
	}
	if err := st.ValidateForLayout(layout); err != nil {
		t.Fatalf("state after AppImage remote icon install: %v", err)
	}
	if installed.State.Current != "1.0" {
		t.Fatalf("current = %q", installed.State.Current)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(themed); !os.IsNotExist(err) {
		t.Fatalf("themed icon remains after uninstall: %v", err)
	}
}

func TestAppImageManifestRefusesArchiveIconAtInstall(t *testing.T) {
	item := &manifest.Manifest{
		Schema: manifest.SchemaV5, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: manifest.Release{Channel: "stable", Version: "1.0", URL: "https://example.com/fixture.AppImage", Verification: manifest.Verification{
			Algorithm: "sha256", Digest: strings.Repeat("a", 64), Source: "https://example.com/fixture.sha256",
		}, Archive: "appimage"},
		ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: "1.0"}}, Releases: []manifest.Release{{
			Channel: "stable", Version: "1.0", URL: "https://example.com/fixture.AppImage", Verification: manifest.Verification{
				Algorithm: "sha256", Digest: strings.Repeat("a", 64), Source: "https://example.com/fixture.sha256",
			}, Archive: "appimage",
		}}},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: "fixture", Path: "appimage"}}},
		Desktop:     manifest.Desktop{Enabled: true, Categories: []string{"Utility"}, Icon: manifest.DesktopIcon{Path: "icon.png"}},
	}
	layout := testLayout(t)
	manager := New(layout, nil)
	if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); err == nil {
		t.Fatal("AppImage with archive-contained icon unexpectedly installed")
	}
	if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
		t.Fatalf("state exists after AppImage archive icon rejection: %v", err)
	}
}

func TestRemoteIconReservedPathRejectsOccupiedAndSymlinkedSources(t *testing.T) {
	layout := testLayout(t)
	icon := []byte("icon bytes")
	routes := map[string][]byte{
		"/fixture-v1.tar.gz":                      fixtureArchive(t, "v1"),
		"/icons/hicolor/512x512/apps/fixture.png": icon,
	}
	server := newMultiRouteServer(t, routes)
	item := server.manifest(t, "v1", routes, "/fixture-v1.tar.gz", "/icons/hicolor/512x512/apps/fixture.png")
	manager := New(layout, &download.Client{HTTP: server.server.Client(), RedirectLimit: 2})
	for name, prepare := range map[string]func(string){
		"regular file": func(root string) {
			if err := os.WriteFile(filepath.Join(root, remoteIconFile), []byte("user owned"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(root string) {
			if err := os.Symlink(t.TempDir(), filepath.Join(root, remoteIconFile)); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			prepare(root)
			if _, _, err := manager.materializeIcon(context.Background(), item, root, nil); !errors.Is(err, ErrConflict) {
				t.Fatalf("materializeIcon() error = %v", err)
			}
		})
	}
}
