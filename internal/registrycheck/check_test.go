package registrycheck

import (
	"archive/tar"
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

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
)

const checkerManifest = `schema: 3
id: fixture
name: Fixture
summary: Fixture application
homepage: https://example.com/
categories: [utilities]
platform: {os: linux, arch: amd64}
release:
  default-channel: stable
  channels:
    stable:
      current: "1.0"
  releases:
    - channel: stable
      version: "1.0"
      url: https://example.com/fixture.tar.gz
      verification:
        algorithm: sha256
        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
        source: https://example.com/SHA256SUMS
      archive: tar.gz
application: {executables: [{name: fixture, path: fixture}]}
desktop: {enabled: false, categories: []}
`

func writeCheckerRegistry(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "apps", "fixture")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "linux-amd64.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeCheckerManifest(t *testing.T, root, id, arch string) {
	t.Helper()
	directory := filepath.Join(root, "apps", id)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(checkerManifest, "id: fixture", "id: "+id)
	body = strings.ReplaceAll(body, "name: Fixture", "name: "+strings.Title(id))
	body = strings.ReplaceAll(body, "arch: amd64", "arch: "+arch)
	if err := os.WriteFile(filepath.Join(directory, "linux-"+arch+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAppSelectsAllArchitecturesDeterministically(t *testing.T) {
	root := t.TempDir()
	writeCheckerManifest(t, root, "k9s", "arm64")
	writeCheckerManifest(t, root, "k9s", "amd64")
	writeCheckerManifest(t, root, "other", "amd64")

	selection, err := App(root, "k9s")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 2 {
		t.Fatalf("selected %d items, want 2", len(selection.Items))
	}
	if got := selection.Items[0].Platform.Arch; got != "amd64" {
		t.Fatalf("first architecture = %q, want amd64", got)
	}
	if got := selection.Items[1].Platform.Arch; got != "arm64" {
		t.Fatalf("second architecture = %q, want arm64", got)
	}
	for _, item := range selection.Items {
		if item.ID != "k9s" {
			t.Fatalf("selected unrelated application %q", item.ID)
		}
	}
}

func TestAppSelectsSingleArchitectureAndRejectsUnknown(t *testing.T) {
	root := writeCheckerRegistry(t, checkerManifest)
	selection, err := App(root, "fixture")
	if err != nil || len(selection.Items) != 1 {
		t.Fatalf("single architecture selection = %#v, error = %v", selection.Items, err)
	}
	if _, err := App(root, "missing"); err == nil {
		t.Fatal("unknown application unexpectedly selected")
	}
}

func TestAppProjectsEveryApprovedHistoricalRelease(t *testing.T) {
	body := strings.Replace(checkerManifest, `application:`, `    - channel: stable
      version: "0.9"
      url: https://example.com/fixture-0.9.tar.gz
      verification:
        algorithm: sha256
        digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
        source: https://example.com/SHA256SUMS-0.9
      archive: tar.gz
application:`, 1)
	root := writeCheckerRegistry(t, body)
	selection, err := App(root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 2 {
		t.Fatalf("selected %d historical releases, want 2", len(selection.Items))
	}
	if selection.Items[0].Release.Version != "0.9" || selection.Items[1].Release.Version != "1.0" {
		t.Fatalf("release projections = %q, %q", selection.Items[0].Release.Version, selection.Items[1].Release.Version)
	}
}

func checkerArchive(t *testing.T, executable, body string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "fixture/"
	entries := []tar.Header{
		{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + executable, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	}
	for _, header := range entries {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(body)); err != nil {
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

func checkerMaterializeManifest(server *httptest.Server, data []byte, executableName, executablePath string) *manifest.Manifest {
	digest := sha256.Sum256(data)
	release := manifest.Release{Channel: "stable", Version: "1.0", URL: server.URL, Verification: manifest.Verification{
		Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.URL + "/SHA256SUMS",
	}, Archive: "tar.gz"}
	return &manifest.Manifest{
		Schema: 3, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: release, ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: "1.0"}}, Releases: []manifest.Release{release}},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: executableName, Path: executablePath}}},
		Desktop:     manifest.Desktop{Enabled: false, Categories: []string{}},
	}
}

func TestMaterializeWithClientLifecycleAndNoExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	data := checkerArchive(t, "bin/run", "#!/bin/sh\necho executed > "+marker+"\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	client := &download.Client{HTTP: server.Client(), RedirectLimit: 2}
	if err := MaterializeWithClient(context.Background(), checkerMaterializeManifest(server, data, "run", "bin/run"), client); err != nil {
		t.Fatalf("MaterializeWithClient() error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("synthetic executable ran: %v", err)
	}
}

func TestMaterializeWithClientReportsMaterializationFailures(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		mutate     func(*manifest.Manifest)
	}{
		{name: "checksum mismatch", executable: "bin/run", mutate: func(item *manifest.Manifest) {
			item.Release.Verification.Digest = strings.Repeat("0", 64)
		}},
		{name: "missing executable", executable: "bin/missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := checkerArchive(t, "bin/run", "not executed")
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(data)
			}))
			defer server.Close()
			item := checkerMaterializeManifest(server, data, "run", test.executable)
			if test.mutate != nil {
				test.mutate(item)
			}
			client := &download.Client{HTTP: server.Client(), RedirectLimit: 2}
			if err := MaterializeWithClient(context.Background(), item, client); err == nil {
				t.Fatal("MaterializeWithClient() unexpectedly succeeded")
			}
		})
	}
}

func TestChangedClassifiesMaterializationAndCatalogChanges(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	newRoot := writeCheckerRegistry(t, strings.Replace(checkerManifest, "Fixture application", "A better summary", 1))
	selection, err := Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 0 {
		t.Fatalf("metadata-only change selected %#v", selection.Items)
	}

	// A release change is represented by appending a new immutable approved
	// release and advancing the channel head; the existing 1.0 entry remains
	// in history.
	changed := strings.Replace(checkerManifest, "      current: \"1.0\"", "      current: \"2.0\"", 1)
	changed = strings.Replace(changed, "application:", `    - channel: stable
      version: "2.0"
      url: https://example.com/fixture-2.0.tar.gz
      verification:
        algorithm: sha256
        digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
        source: https://example.com/SHA256SUMS-2.0
      archive: tar.gz
application:`, 1)
	newRoot = writeCheckerRegistry(t, changed)
	selection, err = Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].ID != "fixture" || selection.Items[0].Release.Version != "2.0" {
		t.Fatalf("artifact change selection = %#v", selection.Items)
	}
}

func TestChangedNewAndDeletedManifests(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := writeCheckerRegistry(t, checkerManifest)
	selection, err := Changed(newRoot, oldRoot)
	if err != nil || len(selection.Items) != 1 {
		t.Fatalf("new manifest selection = %#v, error = %v", selection.Items, err)
	}

	oldRoot = writeCheckerRegistry(t, checkerManifest)
	emptyRoot := t.TempDir()
	if err := Structural(oldRoot); err != nil {
		t.Fatal(err)
	}
	selection, err = Changed(emptyRoot, oldRoot)
	if err == nil {
		t.Fatal("empty current registry unexpectedly accepted")
	}
}

func TestChangedRejectsRemovedV2Manifest(t *testing.T) {
	oldRoot := t.TempDir()
	writeCheckerManifest(t, oldRoot, "fixture", "amd64")
	writeCheckerManifest(t, oldRoot, "fixture", "arm64")
	currentRoot := writeCheckerRegistry(t, checkerManifest)

	if _, err := Changed(currentRoot, oldRoot); err == nil {
		t.Fatal("removed v3 platform manifest unexpectedly accepted")
	}
}

func TestChangedIgnoresRemovedUnreadableV1ManifestDuringMigration(t *testing.T) {
	oldRoot := t.TempDir()
	writeCheckerManifest(t, oldRoot, "fixture", "amd64")
	v1Path := filepath.Join(oldRoot, "apps", "fixture", "linux-amd64.yaml")
	v1, err := os.ReadFile(v1Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(v1Path, []byte(strings.Replace(string(v1), "schema: 3", "schema: 1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	currentRoot := t.TempDir()
	writeCheckerManifest(t, currentRoot, "other", "amd64")
	if _, err := Changed(currentRoot, oldRoot); err != nil {
		t.Fatalf("retired v1 manifest removal rejected: %v", err)
	}
}

func TestChangedDoesNotMaterializeUnchangedArtifactForRetiredSchema(t *testing.T) {
	current := writeCheckerRegistry(t, checkerManifest)
	retired := writeCheckerRegistry(t, strings.Replace(checkerManifest, "schema: 3", "schema: 1", 1))
	selection, err := Changed(current, retired)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 0 {
		t.Fatalf("retired-schema unchanged artifact selected for materialization: %#v", selection.Items)
	}
}

func TestChangedSelectsHistoryArtifactChanges(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	// A new approved release is appended to history. The existing 1.0
	// release remains immutable and retained while the channel head advances.
	changed := strings.Replace(checkerManifest, "      current: \"1.0\"", "      current: \"2.0\"", 1)
	changed = strings.Replace(changed, "application:", `    - channel: stable
      version: "2.0"
      url: https://example.com/fixture-2.0.tar.gz
      verification:
        algorithm: sha256
        digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
        source: https://example.com/SHA256SUMS-2.0
      archive: tar.gz
application:`, 1)
	newRoot := writeCheckerRegistry(t, changed)
	selection, err := Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].Release.Version != "2.0" {
		t.Fatalf("history artifact change selection = %#v", selection.Items)
	}
}

func TestChangedRejectsHistoricalReleaseMutationOrRemoval(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	nestedMutation := strings.Replace(checkerManifest, "      archive: tar.gz\n", "      archive: tar.gz\n      nested-archive: {path: payload.zip, archive: zip}\n", 1)
	if _, err := Changed(writeCheckerRegistry(t, nestedMutation), oldRoot); err == nil {
		t.Fatal("mutated approved nested recipe unexpectedly accepted")
	}
	mutated := strings.Replace(checkerManifest, "https://example.com/fixture.tar.gz", "https://example.com/other.tar.gz", 1)
	if _, err := Changed(writeCheckerRegistry(t, mutated), oldRoot); err == nil {
		t.Fatal("mutated approved release unexpectedly accepted")
	}
	removed := strings.Replace(checkerManifest, "    - channel: stable\n      version: \"1.0\"\n      url: https://example.com/fixture.tar.gz\n      verification:\n        algorithm: sha256\n        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n        source: https://example.com/SHA256SUMS\n      archive: tar.gz\n", "", 1)
	if _, err := Changed(writeCheckerRegistry(t, removed), oldRoot); err == nil {
		t.Fatal("removed approved release unexpectedly accepted")
	}
}

func TestChangedAcceptsExplicitAppImageReleaseCorrection(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	corrected := strings.Replace(checkerManifest, `current: "1.0"`, `current: "1.0-appimage"`, 1)
	corrected = strings.Replace(corrected, `version: "1.0"`, `version: "1.0-appimage"`, 1)
	corrected = strings.Replace(corrected, "https://example.com/fixture.tar.gz", "https://example.com/fixture.AppImage", 1)
	corrected = strings.Replace(corrected, "archive: tar.gz", "archive: appimage", 1)
	corrected = strings.Replace(corrected, "path: fixture", "path: appimage", 1)
	selection, err := Changed(writeCheckerRegistry(t, corrected), oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].Release.Version != "1.0-appimage" {
		t.Fatalf("correction selection = %#v", selection.Items)
	}
}
