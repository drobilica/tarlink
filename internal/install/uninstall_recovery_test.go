package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/drobilica/tarlink/internal/integration"
	"github.com/drobilica/tarlink/internal/state"
)

func TestUninstallRecoveryModifiedDesktop(t *testing.T) {
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
	err := manager.Uninstall(context.Background(), "fixture", nil)
	var conflict *UninstallConflictError
	if !errors.As(err, &conflict) || conflict.Path != desktop {
		t.Fatalf("conflict = %v, typed = %#v", err, conflict)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallRecoveryRejectsArbitraryPathAndPreservesCancelState(t *testing.T) {
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
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", filepath.Join(layout.Desktop, "other.desktop")); !errors.Is(err, integration.ErrConflict) {
		t.Fatalf("arbitrary path error = %v", err)
	}
	if got, _ := os.ReadFile(desktop); string(got) != "changed\n" {
		t.Fatalf("file changed after rejected recovery: %q", got)
	}
	if _, err := state.LoadForApp(layout, "fixture"); err != nil {
		t.Fatalf("state changed before confirmed recovery: %v", err)
	}
}

func TestUninstallRecoverySymlinkAndMultipleConflicts(t *testing.T) {
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
	target := filepath.Join(t.TempDir(), "target")
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
	var conflict *UninstallConflictError
	if !errors.As(err, &conflict) || conflict.Path != bin {
		t.Fatalf("first conflict = %v, typed = %#v", err, conflict)
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", bin); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err == nil {
		t.Fatal("second conflict was skipped")
	}
	if err := manager.RemoveUninstallConflict(context.Background(), "fixture", desktop); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(context.Background(), "fixture", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestUninstallRecoveryDisappearingEntryIsIdempotent(t *testing.T) {
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
