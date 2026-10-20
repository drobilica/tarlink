package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/integrity"
	"github.com/drobilica/tarlink/internal/state"
)

func TestIntegrityBaselinesFollowUpdateRollbackAndRepair(t *testing.T) {
	ctx := context.Background()
	layout := testLayout(t)
	first := newArtifactServer(t, fixtureArchive(t, "1.0"))
	manager := managerFor(t, layout, first)
	assert := func(s state.State) {
		t.Helper()
		for _, ref := range []struct{ version, fingerprint string }{{s.Current, s.CurrentFingerprint}, {s.Previous, s.PreviousFingerprint}} {
			if ref.version == "" {
				continue
			}
			payload, err := layout.PackagePath(s.App, ref.version, ref.fingerprint)
			if err != nil {
				t.Fatal(err)
			}
			changes, err := integrity.Verify(ctx, payload, integrity.Identity{App: s.App, Version: ref.version, Fingerprint: ref.fingerprint})
			if err != nil || len(changes) != 0 {
				t.Fatalf("%s baseline: %v %v", ref.version, changes, err)
			}
		}
	}
	installed, err := manager.InstallWithOptions(ctx, first.manifest("1.0"), Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert(installed.State)
	second := newArtifactServer(t, fixtureArchive(t, "2.0"))
	manager.Client.HTTP = second.server.Client()
	updated, err := manager.UpdateWithOptions(ctx, second.manifest("2.0"), Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assert(updated.State)
	rolled, err := manager.Rollback(ctx, "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	assert(rolled.State)
	payload, _ := layout.PackagePath("fixture", rolled.State.Current, rolled.State.CurrentFingerprint)
	if err := os.Remove(filepath.Join(payload, integrity.Filename)); err != nil {
		t.Fatal(err)
	}
	manager.Client.HTTP = first.server.Client()
	repaired, err := manager.RepairSubject(ctx, first.manifest("1.0"), nil)
	if err != nil {
		t.Fatal(err)
	}
	assert(repaired.State)
}

func TestInstallRejectsPublisherInventoryWithoutActivation(t *testing.T) {
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	for _, e := range []struct{ name, content string }{{"payload/bin/run", "original"}, {"payload/" + integrity.Filename, "forged"}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	layout := testLayout(t)
	server := newArtifactServer(t, data.Bytes())
	manager := managerFor(t, layout, server)
	item := server.manifest("1.0")
	item.Desktop.Icon.Path = ""
	item.Desktop.Enabled = false
	if _, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil); err == nil || !strings.Contains(err.Error(), "reserved integrity") {
		t.Fatalf("collision: %v", err)
	}
	if _, err := state.LoadForApp(layout, "fixture"); !os.IsNotExist(err) {
		t.Fatalf("state published: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(layout.Apps, "fixture", "current")); !os.IsNotExist(err) {
		t.Fatalf("activation published: %v", err)
	}
	entries, err := os.ReadDir(layout.Apps)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-") {
			t.Fatal("failed recording left staging")
		}
	}
}
