package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("second UninstallAll() error = %v", err)
	}
	if _, err := os.Lstat(layout.StateHome); !os.IsNotExist(err) {
		t.Fatalf("no-app purge created state home: %v", err)
	}
}

// TestUninstallAllBlastsThroughCorruptState verifies that a corrupt state file
// no longer aborts the purge: the application roots are removed, the corrupt
// state file is gone, and unrelated user data survives.
func TestUninstallAllBlastsThroughCorruptState(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := os.MkdirAll(layout.States, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(layout.CacheHome, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(layout.CacheHome, "keep")
	if err := os.WriteFile(keep, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(layout.States, "demo.json")
	if err := os.WriteFile(statePath, []byte(`{"schema":3,"app":"demo","channel":"stable","pinned":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.UninstallAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("cache changed after corrupt state: %v", err)
	}
	if _, err := os.Lstat(statePath); !os.IsNotExist(err) {
		t.Fatalf("corrupt state remains: %v", err)
	}
	for _, path := range []string{layout.Apps, layout.States, layout.Locks, layout.Cache} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("TarLink root remains at %s: %v", path, err)
		}
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings for bare corrupt state: %v", result.Warnings)
	}
}

func writeInstalledUninstallFixture(t *testing.T, layout filesystem.Layout, appID string) {
	t.Helper()
	appRoot := filepath.Join(layout.Apps, appID)
	packagePath, err := layout.PackagePath(appID, "v1", testStateFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(packagePath, "bin", "run")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("run"), 0o700); err != nil {
		t.Fatal(err)
	}
	currentTarget, err := filepath.Rel(appRoot, packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(currentTarget, filepath.Join(appRoot, "current")); err != nil {
		t.Fatal(err)
	}
	spec := integration.Spec{
		ID: appID, Name: appID, Executables: []integration.ExecutableSpec{{Name: appID, Path: "bin/run"}}, ApplicationRoot: appRoot,
		LocalBinDirectory: layout.Bin, DesktopDirectory: layout.Desktop, DesktopEnabled: true,
		DesktopCategories: []string{"Utility"},
	}
	spec.DesktopSHA256 = integration.DesktopDigest(spec, integration.ExpectedPaths(spec).Executables[0].Link)
	paths, _, err := integration.Ensure(spec)
	if err != nil {
		t.Fatal(err)
	}
	value := state.State{
		Schema: state.Schema, App: appID, Current: "v1", CurrentFingerprint: testStateFingerprint, Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: appID, Path: "bin/run"}}, DesktopEnabled: true,
		Integration: state.Integration{
			Executables:  []state.ExecutableIntegration{{Name: appID, Path: "bin/run", Link: paths.Executables[0].Link, Target: filepath.Join(appRoot, "current", "bin", "run")}},
			DesktopEntry: paths.DesktopEntry, DesktopSHA256: spec.DesktopSHA256,
		},
	}
	if err := state.WriteForApp(layout, value); err != nil {
		t.Fatal(err)
	}
}

// corruptStateFixture builds a fully installed application and then overwrites
// its state file with one that fails strict decoding. The remaining payload,
// bin link, and desktop entry are the "real remnants" the degraded removal
// path must handle using the layout alone.
func corruptStateFixture(t *testing.T, layout filesystem.Layout, appID string) {
	t.Helper()
	writeInstalledUninstallFixture(t, layout, appID)
	statePath := filepath.Join(layout.States, appID+".json")
	content := fmt.Sprintf(`{"schema":3,"app":%q,"channel":"stable","pinned":false}`, appID)
	if err := os.WriteFile(statePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasUninstallWarning(warnings []string, fragment string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, fragment) {
			return true
		}
	}
	return false
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
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
	if _, err := core.UninstallAll(context.Background(), nil); !errors.Is(err, integration.ErrConflict) {
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
	if _, err := core.UninstallAll(context.Background(), nil); !errors.Is(err, locking.ErrConflict) {
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("retry UninstallAll() error = %v", err)
	}
}

// TestUninstallAllRemovesUnexpectedAppSymlink verifies the purge removes an
// unexpected symlink entry inside the apps root without touching its outside
// target.
func TestUninstallAllRemovesUnexpectedAppSymlink(t *testing.T) {
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
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); !os.IsNotExist(err) {
		t.Fatalf("valid app state remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "unexpected")); !os.IsNotExist(err) {
		t.Fatalf("unexpected symlink entry remains: %v", err)
	}
	if _, err := os.Lstat(layout.Apps); !os.IsNotExist(err) {
		t.Fatalf("apps root remains: %v", err)
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
	if _, err := core.UninstallAll(context.Background(), nil); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("UninstallAll() lifecycle conflict error = %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); err != nil {
		t.Fatalf("state removed while lifecycle lock held: %v", err)
	}
}

// TestUninstallAllBlastsThroughCorruptAppRemnants mixes a valid application
// with a corrupt-state application that still has real remnants. Both are
// removed and unrelated siblings are preserved.
func TestUninstallAllBlastsThroughCorruptAppRemnants(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	writeInstalledUninstallFixture(t, layout, "alpha")
	corruptStateFixture(t, layout, "demo")
	for _, path := range []string{
		filepath.Join(layout.Bin, "keep-tool"), filepath.Join(layout.Desktop, "other.desktop"),
		filepath.Join(filepath.Dir(layout.Apps), "keep-product-data"), filepath.Join(filepath.Dir(layout.States), "keep-product-state"),
	} {
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	// The corrupt app's provable bin link and desktop entry are removed.
	if _, err := os.Lstat(filepath.Join(layout.Bin, "demo")); !os.IsNotExist(err) {
		t.Fatalf("corrupt app bin link remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Desktop, "tarlink-demo.desktop")); !os.IsNotExist(err) {
		t.Fatalf("corrupt app desktop entry remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Bin, "alpha")); !os.IsNotExist(err) {
		t.Fatalf("valid app bin link remains: %v", err)
	}
	if _, err := state.LoadForApp(layout, "alpha"); !os.IsNotExist(err) {
		t.Fatalf("valid app state remains: %v", err)
	}
	for _, path := range []string{layout.Apps, layout.States, layout.Locks, layout.Cache} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("TarLink root remains at %s: %v", path, err)
		}
	}
	for _, path := range []string{
		filepath.Join(layout.Bin, "keep-tool"), filepath.Join(layout.Desktop, "other.desktop"),
		filepath.Join(filepath.Dir(layout.Apps), "keep-product-data"), filepath.Join(filepath.Dir(layout.States), "keep-product-state"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("shared path %s changed: %v", path, err)
		}
	}
}

// TestUninstallAllCorruptAppLeavesOutsideBinSymlink verifies a bin symlink
// pointing outside the payload survives the degraded removal.
func TestUninstallAllCorruptAppLeavesOutsideBinSymlink(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(layout.Bin, "evil")); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	if _, err := core.UninstallAll(context.Background(), nil); err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	linkPath := filepath.Join(layout.Bin, "evil")
	if _, err := os.Lstat(linkPath); err != nil {
		t.Fatalf("outside bin symlink changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("outside target changed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "demo")); !os.IsNotExist(err) {
		t.Fatalf("corrupt app payload remains: %v", err)
	}
}

// TestUninstallAllCorruptAppKeepsOutsideDesktopReference verifies a canonical
// desktop entry carrying the marker but referencing an outside executable is
// kept and reported as a warning.
func TestUninstallAllCorruptAppKeepsOutsideDesktopReference(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	outside := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(outside, []byte("tool"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(layout.Desktop, "tarlink-demo.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Demo\nExec=" + outside + "\nX-TarLink-AppID=demo\n"
	if err := os.WriteFile(entry, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.UninstallAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := os.Lstat(entry); err != nil {
		t.Fatalf("outside desktop entry removed: %v", err)
	}
	if !hasUninstallWarning(result.Warnings, "tarlink-demo.desktop") {
		t.Fatalf("desktop entry not reported in warnings: %v", result.Warnings)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside target changed: %v", err)
	}
}

// TestUninstallAllCorruptAppKeepsUnmarkedDesktopEntry verifies a canonical
// desktop entry without the TarLink marker is kept with a warning even when
// its Exec value sits inside the payload.
func TestUninstallAllCorruptAppKeepsUnmarkedDesktopEntry(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	entry := filepath.Join(layout.Desktop, "tarlink-demo.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Demo\nExec=" + filepath.Join(layout.Apps, "demo", "current", "bin", "run") + "\n"
	if err := os.WriteFile(entry, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.UninstallAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := os.Lstat(entry); err != nil {
		t.Fatalf("unmarked desktop entry removed: %v", err)
	}
	if !hasUninstallWarning(result.Warnings, "tarlink-demo.desktop") {
		t.Fatalf("unmarked desktop entry not reported: %v", result.Warnings)
	}
}

// TestUninstallAllCorruptAppReportsIconLeftover verifies icons left under the
// hicolor tree survive and are listed in the warnings.
func TestUninstallAllCorruptAppReportsIconLeftover(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	icon := filepath.Join(layout.Icons, "48x48", "apps", "tarlink-demo.png")
	if err := os.MkdirAll(filepath.Dir(icon), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(icon, []byte("icon"), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.UninstallAll(context.Background(), nil)
	if err != nil {
		t.Fatalf("UninstallAll() error = %v", err)
	}
	if _, err := os.Stat(icon); err != nil {
		t.Fatalf("leftover icon removed: %v", err)
	}
	if !hasUninstallWarning(result.Warnings, "tarlink-demo.png") {
		t.Fatalf("leftover icon not reported: %v", result.Warnings)
	}
}

// TestUninstallCorruptStateBlastsThrough verifies the single-application
// uninstall removes a corrupt application's payload, state, and provable
// integrations without warnings.
func TestUninstallCorruptStateBlastsThrough(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.Uninstall(context.Background(), "demo", nil)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", result.Warnings)
	}
	if _, err := os.Lstat(filepath.Join(layout.Bin, "demo")); !os.IsNotExist(err) {
		t.Fatalf("bin link remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Desktop, "tarlink-demo.desktop")); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "demo")); !os.IsNotExist(err) {
		t.Fatalf("payload remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.States, "demo.json")); !os.IsNotExist(err) {
		t.Fatalf("state remains: %v", err)
	}
}

// TestUninstallCorruptStateKeepsHostilePaths verifies the single-application
// uninstall removes the payload and state while keeping hostile integration
// paths that cannot be proven owned, and reports them as warnings.
func TestUninstallCorruptStateKeepsHostilePaths(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(layout.Bin, "evil")); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(layout.Desktop, "tarlink-demo.desktop")
	if err := os.WriteFile(entry, []byte("[Desktop Entry]\nType=Application\nName=Demo\nExec="+outside+"\nX-TarLink-AppID=demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.Uninstall(context.Background(), "demo", nil)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Bin, "evil")); err != nil {
		t.Fatalf("outside bin symlink changed: %v", err)
	}
	if _, err := os.Lstat(entry); err != nil {
		t.Fatalf("hostile desktop entry removed: %v", err)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected warnings for kept hostile paths, got none")
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "demo")); !os.IsNotExist(err) {
		t.Fatalf("payload remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.States, "demo.json")); !os.IsNotExist(err) {
		t.Fatalf("state remains: %v", err)
	}
}

// TestUninstallCorruptStateKeepsSymlinkedDesktopRoot verifies the degraded
// desktop-entry removal refuses to act when the desktop directory itself is a
// symlink, even when the entry carries the marker and references the payload.
func TestUninstallCorruptStateKeepsSymlinkedDesktopRoot(t *testing.T) {
	layout := uninstallTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	corruptStateFixture(t, layout, "demo")
	outside := t.TempDir()
	entry := filepath.Join(layout.Desktop, "tarlink-demo.desktop")
	content := "[Desktop Entry]\nType=Application\nName=Demo\nExec=" +
		filepath.Join(layout.Apps, "demo", "current", "bin", "run") + "\nX-TarLink-AppID=demo\n"
	if err := os.WriteFile(entry, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry, filepath.Join(outside, "tarlink-demo.desktop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(layout.Desktop); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, layout.Desktop); err != nil {
		t.Fatal(err)
	}
	core := &Core{layout: layout, installer: install.New(layout, nil)}
	result, err := core.Uninstall(context.Background(), "demo", nil)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "tarlink-demo.desktop")); err != nil {
		t.Fatalf("entry behind symlinked desktop root changed: %v", err)
	}
	if !hasUninstallWarning(result.Warnings, "tarlink-demo.desktop") {
		t.Fatalf("symlinked desktop root not reported: %v", result.Warnings)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "demo")); !os.IsNotExist(err) {
		t.Fatalf("payload remains: %v", err)
	}
}
