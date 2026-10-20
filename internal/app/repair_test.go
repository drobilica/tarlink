package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/drobilica/tarlink/internal/state"
)

func repairServiceArtifact(t *testing.T, content string) []byte {
	t.Helper()
	var output bytes.Buffer
	gz := gzip.NewWriter(&output)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "bravo", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
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

func TestRepairResolvesOfficialExactIdentity(t *testing.T) {
	ctx := context.Background()
	artifacts := map[string][]byte{"bravo": repairServiceArtifact(t, "approved")}
	core, _ := repositoryServiceCore(t, repositoryServiceArchive(t, artifacts), artifacts)
	if _, err := core.Install(ctx, "bravo", nil); err != nil {
		t.Fatal(err)
	}
	if err := core.Pin(ctx, "bravo"); err != nil {
		t.Fatal(err)
	}
	before, err := state.LoadForApp(core.layout, "bravo")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(core.layout.Apps, "bravo", "current", "bravo")
	if err := os.WriteFile(path, []byte("damaged"), 0o700); err != nil {
		t.Fatal(err)
	}
	result, err := core.Repair(ctx, "bravo", nil)
	if err != nil || !result.Pinned || result.Fingerprint != before.CurrentFingerprint {
		t.Fatalf("repair = %+v %v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "approved" {
		t.Fatalf("repaired content = %q %v", content, err)
	}
	after, err := state.LoadForApp(core.layout, "bravo")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: %+v %v", after, err)
	}
	if report, err := core.Doctor(ctx); err != nil || report.Errors != 0 {
		t.Fatalf("doctor after repair = %+v %v", report, err)
	}
	// A new registry may keep the version string while changing package bytes.
	// Repair must refuse that substitution, even if the version is still listed.
	replacement := map[string][]byte{"bravo": repairServiceArtifact(t, "replacement")}
	changed, _ := repositoryServiceCore(t, repositoryServiceArchive(t, replacement), replacement)
	core.syncer.Client = changed.syncer.Client
	if _, err := core.SyncRegistry(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Repair(ctx, "bravo", nil); err == nil {
		t.Fatal("repair accepted changed registry fingerprint")
	}
	content, err = os.ReadFile(path)
	if err != nil || string(content) != "approved" {
		t.Fatalf("refused repair changed payload: %q %v", content, err)
	}
	// A version removed from approved history must not be restored from cache.
	unavailable, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "2.0")))
	core.syncer.Client = unavailable.syncer.Client
	if _, err := core.SyncRegistry(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Repair(ctx, "bravo", nil); err == nil {
		t.Fatal("repair accepted an application absent from the catalog")
	}

}

func TestRepairRejectsInvalidAndMissingApplication(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if _, err := core.Repair(context.Background(), "../unsafe", nil); CodeOf(err) != CodeInvalidArguments {
		t.Fatalf("invalid ID: %v", err)
	}
	if _, err := core.Repair(context.Background(), "fixture@stable", nil); CodeOf(err) != CodeInvalidArguments {
		t.Fatalf("selector accepted: %v", err)
	}
	if _, err := core.Repair(context.Background(), "fixture", nil); CodeOf(err) != CodeNotInstalled {
		t.Fatalf("missing app: %v", err)
	}
}
