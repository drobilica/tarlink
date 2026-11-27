package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/state"
)

// activationFailureStages are the failure points a materialized activation
// passes through. "write_state" fails the state write before it commits.
var activationFailureStages = []string{"after_download", "after_extract", "after_integration", "after_activation", "before_state", "write_state"}

// snapshotInstallation records every entry below the directories a failed
// install or update may touch (payloads, state, bin links, desktop entries,
// and icons) so a rollback can be compared with the exact earlier tree.
// Disposable cache and lock files are deliberately excluded, as are the
// shared hicolor theme directories, which integration creates on demand and
// does not prune; icon files inside them are still recorded.
func snapshotInstallation(t *testing.T, layout filesystem.Layout) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	for _, root := range []string{layout.Apps, layout.States, layout.Bin, layout.Desktop, layout.Icons} {
		if _, err := os.Lstat(root); os.IsNotExist(err) && root == layout.Icons {
			continue
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if root == layout.Icons && entry.IsDir() {
				return nil
			}
			value := info.Mode().String()
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				target, err := os.Readlink(path)
				if err != nil {
					return err
				}
				value += " -> " + target
			case info.Mode().IsRegular():
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				digest := sha256.Sum256(content)
				value += " " + hex.EncodeToString(digest[:])
			}
			snapshot[path] = value
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}

func failActivationAt(manager *Manager, stage string, injected error) {
	if stage == "write_state" {
		manager.writeState = func(filesystem.Layout, state.State) (bool, error) { return false, injected }
		return
	}
	manager.fail = func(current string) error {
		if current == stage {
			return injected
		}
		return nil
	}
}

func TestFailedInstallAtEachStageLeavesNoInstallation(t *testing.T) {
	for _, stage := range activationFailureStages {
		t.Run(stage, func(t *testing.T) {
			layout := testLayout(t)
			server := newArtifactServer(t, fixtureArchive(t, "v1"))
			manager := managerFor(t, layout, server)
			before := snapshotInstallation(t, layout)
			injected := errors.New("injected " + stage + " failure")
			failActivationAt(manager, stage, injected)

			outcome, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil)
			if !errors.Is(err, injected) || errors.Is(err, integration.ErrRollbackIncomplete) {
				t.Fatalf("Install() error = %v, want only the injected failure", err)
			}
			if !reflect.DeepEqual(outcome, Outcome{}) {
				t.Fatalf("Install() outcome = %#v, want zero", outcome)
			}
			if _, err := os.Lstat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
				t.Fatalf("application root remains: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(layout.Desktop, "tarlink-fixture.desktop")); !os.IsNotExist(err) {
				t.Fatalf("desktop entry remains: %v", err)
			}
			if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
				t.Fatalf("state exists after failed install: %v", err)
			}
			if after := snapshotInstallation(t, layout); !reflect.DeepEqual(after, before) {
				t.Fatalf("installation tree changed after failed install:\nbefore %v\nafter  %v", before, after)
			}

			manager.fail = nil
			manager.writeState = nil
			if _, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil); err != nil {
				t.Fatalf("Install() after rollback error = %v", err)
			}
			assertCurrent(t, layout, "fixture", "v1")
		})
	}
}

func TestFailedUpdateAtEachStageRestoresInstallation(t *testing.T) {
	for _, stage := range activationFailureStages {
		t.Run(stage, func(t *testing.T) {
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
			before := snapshotInstallation(t, layout)
			v3Server := newArtifactServer(t, fixtureArchive(t, "v3"))
			manager.Client = &download.Client{HTTP: v3Server.server.Client(), RedirectLimit: 2}
			injected := errors.New("injected " + stage + " failure")
			failActivationAt(manager, stage, injected)

			outcome, err := manager.UpdateWithOptions(context.Background(), v3Server.manifest("v3"), Options{Channel: "stable"}, nil)
			if !errors.Is(err, injected) || errors.Is(err, integration.ErrRollbackIncomplete) {
				t.Fatalf("Update() error = %v, want only the injected failure", err)
			}
			if !reflect.DeepEqual(outcome, Outcome{}) {
				t.Fatalf("Update() outcome = %#v, want zero", outcome)
			}
			assertCurrent(t, layout, "fixture", "v2")
			installed, err := state.LoadForApp(layout, "fixture")
			if err != nil || installed.Current != "v2" || installed.Previous != "v1" {
				t.Fatalf("state = %#v, %v", installed, err)
			}
			if _, err := os.Stat(filepath.Join(layout.Desktop, "tarlink-fixture.desktop")); err != nil {
				t.Fatalf("desktop entry missing after failed update: %v", err)
			}
			if after := snapshotInstallation(t, layout); !reflect.DeepEqual(after, before) {
				t.Fatalf("installation tree changed after failed update:\nbefore %v\nafter  %v", before, after)
			}

			manager.fail = nil
			manager.writeState = nil
			updated, err := manager.UpdateWithOptions(context.Background(), v3Server.manifest("v3"), Options{Channel: "stable"}, nil)
			if err != nil {
				t.Fatalf("Update() after rollback error = %v", err)
			}
			if updated.State.Current != "v3" || updated.State.Previous != "v2" || len(updated.Warnings) != 0 {
				t.Fatalf("Update() after rollback outcome = %#v", updated)
			}
			assertCurrent(t, layout, "fixture", "v3")
			oldPath, err := layout.PackagePath("fixture", "v1", installed.PreviousFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(oldPath); !os.IsNotExist(err) {
				t.Fatalf("superseded previous version remains: %v", err)
			}
		})
	}
}

// lockDirectoryAt makes path read-only when the activation reaches stage, so
// the rollback that follows the injected failure cannot change it.
func lockDirectoryAt(t *testing.T, manager *Manager, stage string, injected error, paths ...string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	t.Cleanup(func() {
		for _, path := range paths {
			_ = os.Chmod(path, 0o700)
		}
	})
	manager.fail = func(current string) error {
		if current != stage {
			return nil
		}
		for _, path := range paths {
			if err := os.Chmod(path, 0o500); err != nil {
				t.Fatal(err)
			}
		}
		return injected
	}
}

func TestFailedInstallRollsBackActivationBeforeIntegration(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	injected := errors.New("injected activation failure")
	lockDirectoryAt(t, manager, "after_activation", injected, filepath.Join(layout.Apps, "fixture"), layout.Desktop)

	_, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil)
	if !errors.Is(err, injected) || !errors.Is(err, integration.ErrRollbackIncomplete) {
		t.Fatalf("Install() error = %v, want the injected failure and ErrRollbackIncomplete", err)
	}
	// The current-link restore runs first and is joined first; the integration
	// cleanup runs after it and wraps the result.
	var outer *integration.RollbackError
	if !errors.As(err, &outer) {
		t.Fatalf("Install() error = %T, want *integration.RollbackError", err)
	}
	var inner *integration.RollbackError
	if !errors.As(outer.Err, &inner) || inner.Err != injected {
		t.Fatalf("Install() error = %v, want the current-link restore failure nested inside the integration cleanup failure", err)
	}
	if inner.Rollback == nil || !strings.Contains(inner.Rollback.Error(), filepath.Join(layout.Apps, "fixture", "current")) {
		t.Fatalf("inner rollback error = %v, want the current-link restore failure", inner.Rollback)
	}
	if outer.Rollback == nil || !strings.Contains(outer.Rollback.Error(), layout.Desktop) {
		t.Fatalf("outer rollback error = %v, want the desktop integration cleanup failure", outer.Rollback)
	}
}

func TestFailedInstallReportsIncompleteIntegrationCleanupAndStillRemovesPayload(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	injected := errors.New("injected integration failure")
	lockDirectoryAt(t, manager, "after_integration", injected, layout.Desktop)

	_, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, nil)
	if !errors.Is(err, injected) || !errors.Is(err, integration.ErrRollbackIncomplete) {
		t.Fatalf("Install() error = %v, want the injected failure and ErrRollbackIncomplete", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
		t.Fatalf("application root remains after integration cleanup failure: %v", err)
	}
}

func TestFailedInstallPayloadCleanupIsBestEffort(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	item := server.manifest("v1")
	fingerprint, err := item.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	packagePath, err := layout.PackagePath("fixture", "v1", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected state failure")
	lockDirectoryAt(t, manager, "before_state", injected, packagePath)

	_, err = manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
	if !errors.Is(err, injected) || errors.Is(err, integration.ErrRollbackIncomplete) {
		t.Fatalf("Install() error = %v, want only the injected failure", err)
	}
	if _, err := os.Lstat(packagePath); err != nil {
		t.Fatalf("locked package directory = %v, want it left in place", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "fixture", "current")); !os.IsNotExist(err) {
		t.Fatalf("current link remains: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Desktop, "tarlink-fixture.desktop")); !os.IsNotExist(err) {
		t.Fatalf("desktop entry remains: %v", err)
	}
	if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
		t.Fatalf("state exists after failed install: %v", err)
	}
}
