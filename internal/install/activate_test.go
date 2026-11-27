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
	"github.com/drobilica/tarlink/internal/manifest"
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
	// Undo steps are joined in the order they run: the current-link restore
	// first, then the integration cleanup, then the removal of the version
	// directory this install created (which also fails here because the app
	// root is read-only). Each wraps the previous.
	layers := []string{}
	cause := err
	for {
		var rollback *integration.RollbackError
		if !errors.As(cause, &rollback) {
			break
		}
		layers = append([]string{rollback.Rollback.Error()}, layers...)
		cause = rollback.Err
	}
	if cause != injected || len(layers) != 3 {
		t.Fatalf("Install() error = %v, want three rollback layers around the injected failure", err)
	}
	for index, want := range []string{
		filepath.Join(layout.Apps, "fixture", "current"),
		layout.Desktop,
		filepath.Join(layout.Apps, "fixture"),
	} {
		if !strings.Contains(layers[index], want) {
			t.Fatalf("rollback layer %d = %q, want it to mention %s", index, layers[index], want)
		}
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

func TestFailedInstallReportsPayloadLeftBehind(t *testing.T) {
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
	// The package directory could not be removed: the leftover would block the
	// next install as an untracked directory, so the failure must be reported.
	if !errors.Is(err, injected) || !errors.Is(err, integration.ErrRollbackIncomplete) {
		t.Fatalf("Install() error = %v, want the injected failure and ErrRollbackIncomplete", err)
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

func TestFailedPublishRemovesCreatedVersionAndApplicationRoots(t *testing.T) {
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "v1"))
	manager := managerFor(t, layout, server)
	// "installing" is reported after the version directory is created and
	// immediately before the staged payload is renamed into it; removing the
	// stage there makes the rename fail.
	progress := Progress(func(stage string, _, _ int64) {
		if stage != "installing" {
			return
		}
		stages, err := filepath.Glob(filepath.Join(layout.Apps, ".staging-fixture-*"))
		if err != nil || len(stages) != 1 {
			t.Fatalf("staging directories = %v, %v", stages, err)
		}
		if err := os.RemoveAll(stages[0]); err != nil {
			t.Fatal(err)
		}
	})

	_, err := manager.InstallWithOptions(context.Background(), server.manifest("v1"), Options{Channel: "stable"}, progress)
	if err == nil || !strings.Contains(err.Error(), "publish version") || errors.Is(err, integration.ErrRollbackIncomplete) {
		t.Fatalf("Install() error = %v, want only the publish failure", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "fixture")); !os.IsNotExist(err) {
		t.Fatalf("application root remains after failed publish: %v", err)
	}
}

func TestUpdateRecordsPreviousRuntime(t *testing.T) {
	manager, item, repository, objects, _ := offlineClosureFixture(t)
	for _, object := range objects {
		offlinePublish(t, repository, object)
	}
	ctx := context.Background()
	if _, err := manager.InstallWithOptions(ctx, item, Options{Channel: "stable"}, nil); err != nil {
		t.Fatal(err)
	}
	firstRuntime := *item.Runtime

	application := fixtureArchive(t, "offline-2")
	applicationDigest := sha256.Sum256(application)
	applicationObject := offlineObject{digest: hex.EncodeToString(applicationDigest[:]), body: application}
	offlinePublish(t, repository, applicationObject)
	next := *item
	nextRuntime := firstRuntime
	nextRuntime.Version = "2.0"
	nextReference := &manifest.RuntimeReference{ID: nextRuntime.ID, Version: nextRuntime.Version}
	next.Release.Version = "2.0"
	next.Release.Verification.Digest = applicationObject.digest
	next.RuntimeRef, next.Runtime = nextReference, &nextRuntime
	next.Release.RuntimeRef, next.Release.Runtime = nextReference, &nextRuntime
	next.ReleaseHistory.Channels = map[string]manifest.ChannelHead{"stable": {Current: "2.0"}}
	next.ReleaseHistory.Releases = []manifest.Release{next.Release}

	updated, err := manager.UpdateWithOptions(ctx, &next, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State.PreviousRuntime == nil || !reflect.DeepEqual(*updated.State.PreviousRuntime, firstRuntime) {
		t.Fatalf("previous runtime = %#v, want %#v", updated.State.PreviousRuntime, firstRuntime)
	}
	if updated.State.Runtime == nil || !reflect.DeepEqual(*updated.State.Runtime, nextRuntime) {
		t.Fatalf("runtime = %#v, want %#v", updated.State.Runtime, nextRuntime)
	}
	if updated.State.Runtime == next.Runtime {
		t.Fatal("state runtime aliases the manifest runtime")
	}
	stored, err := state.LoadForApp(manager.Layout, item.ID)
	if err != nil || !reflect.DeepEqual(stored.PreviousRuntime, updated.State.PreviousRuntime) {
		t.Fatalf("stored previous runtime = %#v, %v", stored.PreviousRuntime, err)
	}
}

func TestUpdateReportsSupersededPackageCleanupFailureOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	layout := testLayout(t)
	manager := managerFor(t, layout, newArtifactServer(t, nil))
	for _, version := range []string{"v1", "v2"} {
		server := newArtifactServer(t, fixtureArchive(t, version))
		manager.Client = &download.Client{HTTP: server.server.Client(), RedirectLimit: 2}
		var err error
		if version == "v1" {
			_, err = manager.InstallWithOptions(context.Background(), server.manifest(version), Options{Channel: "stable"}, nil)
		} else {
			_, err = manager.UpdateWithOptions(context.Background(), server.manifest(version), Options{Channel: "stable"}, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	superseded := filepath.Join(layout.Apps, "fixture", "v1")
	if err := os.Chmod(superseded, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(superseded, 0o700) })

	v3Server := newArtifactServer(t, fixtureArchive(t, "v3"))
	manager.Client = &download.Client{HTTP: v3Server.server.Client(), RedirectLimit: 2}
	updated, err := manager.UpdateWithOptions(context.Background(), v3Server.manifest("v3"), Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("Update() error = %v, want the committed update to succeed", err)
	}
	if len(updated.Warnings) != 1 || !strings.Contains(updated.Warnings[0], superseded) {
		t.Fatalf("Update() warnings = %q, want one cleanup warning for %s", updated.Warnings, superseded)
	}
	if updated.State.Current != "v3" || updated.State.Previous != "v2" {
		t.Fatalf("state = %#v", updated.State)
	}
	assertCurrent(t, layout, "fixture", "v3")
}
