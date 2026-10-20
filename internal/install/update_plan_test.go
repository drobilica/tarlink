package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPlannedUpdateRechecksStateUnderApplicationLock(t *testing.T) {
	ctx := context.Background()
	layout := testLayout(t)
	server := newArtifactServer(t, fixtureArchive(t, "1.0"))
	manager := managerFor(t, layout, server)
	outcome, err := manager.InstallWithOptions(ctx, server.manifest("1.0"), Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetPinned(ctx, "fixture", true); err != nil {
		t.Fatal(err)
	}
	replacement := newArtifactServer(t, fixtureArchive(t, "2.0"))
	manager.Client.HTTP = replacement.server.Client()
	replacement.server.Close() // Any artifact access would fail for another reason.
	err = manager.WithLifecycle(ctx, func() error {
		_, err := manager.UpdatePlannedUnderLifecycle(ctx, replacement.manifest("2.0"), Options{Channel: "stable"}, outcome.State, nil)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale state was not rejected: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(layout.Apps, "fixture", "current", "bin/run"))
	if err != nil || string(body) != "1.0" {
		t.Fatalf("stale update changed payload: %q %v", body, err)
	}
}
