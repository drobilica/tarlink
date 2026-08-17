package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/locking"
	"github.com/drobilica/tarlink/internal/state"
)

func uninstallTestLayout(t *testing.T) filesystem.Layout {
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
	return layout
}

func TestUninstallAllWithNoApplicationsIsIdempotent(t *testing.T) {
	layout := uninstallTestLayout(t)
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("second UninstallAll() error = %v", err)
	}
	if _, err := os.Lstat(layout.StateHome); !os.IsNotExist(err) {
		t.Fatalf("no-app purge created state home: %v", err)
	}
}

func TestUninstallAllRejectsCorruptStateBeforeCleanup(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := os.MkdirAll(layout.States, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.Cache, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(layout.Cache, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(layout.States, "demo.json")
	if err := os.WriteFile(statePath, []byte(`{"schema":1,"app":"demo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	err := core.UninstallAll(context.Background(), nil)
	if !errors.Is(err, state.ErrCorrupt) {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("cache changed after corrupt state: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("corrupt state removed: %v", err)
	}
}

func writeInstalledUninstallFixture(t *testing.T, layout filesystem.Layout, appID string) {
	t.Helper()
	appRoot := filepath.Join(layout.Apps, appID)
	executable := filepath.Join(appRoot, "v1", "bin", "run")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("v1", filepath.Join(appRoot, "current")); err != nil {
		t.Fatal(err)
	}
	spec := integration.Spec{
		ID: appID, Name: appID, Executable: "bin/run", ApplicationRoot: appRoot,
		LocalBinDirectory: layout.Bin, DesktopDirectory: layout.Desktop, DesktopEnabled: true,
		DesktopCategories: []string{"Utility"},
	}
	spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).ExecutableLink)
	paths, _, err := integration.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	value := state.State{
		Schema: state.Schema, App: appID, Current: "v1", Executable: "bin/run", DesktopEnabled: true,
		Integration: state.Integration{
			ExecutableLink: paths.ExecutableLink, ExecutableTarget: filepath.Join(appRoot, "current", "bin", "run"),
			DesktopEntry: paths.DesktopEntry, DesktopSHA256: spec.DesktopSHA256,
		},
	}
	if err := state.WriteForApp(layout, value); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallAllPurgesApplicationsAndPreservesSharedSiblings(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	writeInstalledUninstallFixture(t, layout, "beta")
	for _, path := range []string{
		filepath.Join(layout.DataHome, "keep-data"), filepath.Join(layout.StateHome, "keep-state"),
		filepath.Join(layout.CacheHome, "keep-cache"), filepath.Join(layout.Bin, "keep-tool"),
		filepath.Join(layout.Desktop, "other.desktop"), filepath.Join(filepath.Dir(layout.Apps), "keep-product-data"),
		filepath.Join(filepath.Dir(layout.States), "keep-product-state"),
	} {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	for _, path := range []string{layout.Apps, layout.States, layout.Locks, layout.Cache} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("TarLink root remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(layout.DataHome, "keep-data"), filepath.Join(layout.StateHome, "keep-state"),
		filepath.Join(layout.CacheHome, "keep-cache"), filepath.Join(layout.Bin, "keep-tool"),
		filepath.Join(layout.Desktop, "other.desktop"), filepath.Join(filepath.Dir(layout.Apps), "keep-product-data"),
		filepath.Join(filepath.Dir(layout.States), "keep-product-state"), layout.DataHome, layout.StateHome, layout.CacheHome,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("shared path %s changed: %v", path, err)
		}
	}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("second UninstallAll() error = %v", err)
	}
}

func TestUninstallAllRemovesEmptyProductParents(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	for _, path := range []string{filepath.Dir(layout.Apps), filepath.Dir(layout.States)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("empty product parent remains at %s: %v", path, err)
		}
	}
}

func TestUninstallAllPreflightsIntegrationConflicts(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	writeInstalledUninstallFixture(t, layout, "beta")
	paths := filepath.Join(layout.Desktop, "tarlink-beta.desktop")
	content, err := os.ReadFile(paths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths, append(content, []byte("Comment=changed\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if err := core.UninstallAll(context.Background(), nil); !errors.Is(err, integration.ErrConflict) {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	for _, appID := range []string{"alpha", "beta"} {
		if _, err := state.LoadForApp(layout, appID); err != nil {
			t.Fatalf("state %s changed after conflict: %v", appID, err)
		}
	}
}

func TestUninstallAllRetriesAfterPartialPerAppFailure(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	writeInstalledUninstallFixture(t, layout, "beta")
	manager := install.New(layout, nil)
	manager.LockTimeout = 20 * time.Millisecond
	held, err := locking.AcquireApp(context.Background(), layout.Locks, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: manager}
	if err := core.UninstallAll(context.Background(), nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); err != nil {
		t.Fatalf("failed app state missing: %v", err)
	}
	if _, err := state.LoadForApp(layout, "beta"); !os.IsNotExist(err) {
		t.Fatalf("successful app state remains: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("retry UninstallAll() error = %v", err)
	}
}

func TestUninstallAllRejectsUnexpectedAppSymlink(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(layout.Apps, "unexpected")); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if err := core.UninstallAll(context.Background(), nil); !errors.Is(err, state.ErrCorrupt) {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); err != nil {
		t.Fatalf("state removed after symlink rejection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside data changed: %v", err)
	}
}

func TestUninstallAllRespectsLifecycleDirectoryLock(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	manager := install.New(layout, nil)
	manager.LockTimeout = 20 * time.Millisecond
	held, err := locking.AcquireDirectoryWithTimeout(context.Background(), layout.Home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	core := &Core{layout: layout, installer: manager}
	if err := core.UninstallAll(context.Background(), nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("UninstallAll() lifecycle conflict error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); err != nil {
		t.Fatalf("state removed while lifecycle lock held: %v", err)
	}
}
