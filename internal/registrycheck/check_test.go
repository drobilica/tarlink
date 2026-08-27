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

const checkerManifest = `schema: 5
id: fixture
name: Fixture
summary: Fixture application
homepage: https://example.com/
categories: [utilities]
release:
  current: "1.0"
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: "1.0"
      artifacts:
        linux-amd64:
          url: https://example.com/fixture.tar.gz
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/SHA256SUMS
application:
  executable:
    name: fixture
    path: fixture
`

func checkerDualPlatform() string {
	return strings.Replace(checkerManifest, "application:\n",
		"        linux-arm64:\n"+
			"          url: https://example.com/fixture-arm64.tar.gz\n"+
			"          verification:\n"+
			"            digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n"+
			"            source: https://example.com/SHA256SUMS\n"+
			"application:\n", 1)
}

func writeCheckerRegistry(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "apps", "fixture")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func checkerArchive(t *testing.T, executable, body string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []tar.Header{
		{Name: "fixture/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "fixture/" + executable, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
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
		Schema: manifest.SchemaV5, ID: "fixture", Name: "Fixture", Summary: "Fixture application", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: release, ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: "1.0"}}, Releases: []manifest.Release{release}},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: executableName, Path: executablePath}}},
		Desktop:     manifest.Desktop{Categories: []string{}},
	}
}

func TestAppSelectsExactArchitectures(t *testing.T) {
	selection, err := App(writeCheckerRegistry(t, checkerDualPlatform()), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 2 || selection.Items[0].Platform.Arch != "amd64" || selection.Items[1].Platform.Arch != "arm64" {
		t.Fatalf("selection = %#v", selection.Items)
	}
}

func TestAppProjectsEveryApprovedHistoricalRelease(t *testing.T) {
	body := strings.Replace(checkerManifest, "application:\n", "    - version: \"0.9\"\n      artifacts:\n        linux-amd64:\n          url: https://example.com/fixture-0.9.tar.gz\n          verification:\n            digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789\n            source: https://example.com/SHA256SUMS-0.9\napplication:\n", 1)
	selection, err := App(writeCheckerRegistry(t, body), "fixture")
	if err != nil || len(selection.Items) != 2 {
		t.Fatalf("selection = %#v, error = %v", selection.Items, err)
	}
	if selection.Items[0].Release.Version != "0.9" || selection.Items[1].Release.Version != "1.0" {
		t.Fatalf("release projections = %#v", selection.Items)
	}
}

func TestMaterializeWithClientLifecycleAndNoExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	data := checkerArchive(t, "bin/run", "#!/bin/sh\necho executed > "+marker+"\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(data) }))
	defer server.Close()
	if err := MaterializeWithClient(context.Background(), checkerMaterializeManifest(server, data, "run", "bin/run"), &download.Client{HTTP: server.Client(), RedirectLimit: 2}); err != nil {
		t.Fatalf("MaterializeWithClient() error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("synthetic executable ran: %v", err)
	}
}

func TestChangedFingerprintAndMetadataBehavior(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	for name, body := range map[string]string{
		"summary": strings.Replace(checkerManifest, "Fixture application", "A better summary", 1),
		"source":  strings.Replace(checkerManifest, "https://example.com/SHA256SUMS", "https://example.com/other-source", 1),
		"digest":  strings.Replace(checkerManifest, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", 1),
	} {
		selection, err := Changed(writeCheckerRegistry(t, body), oldRoot)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := 0
		if name == "digest" {
			want = 1
		}
		if len(selection.Items) != want {
			t.Fatalf("%s selected %d items, want %d", name, len(selection.Items), want)
		}
	}
	archive := strings.Replace(checkerManifest, "archive: tar.gz", "archive: appimage", 1)
	archive = strings.Replace(archive, "path: fixture", "path: appimage", 1)
	if selection, err := Changed(writeCheckerRegistry(t, archive), oldRoot); err != nil || len(selection.Items) != 1 {
		t.Fatalf("archive change selection = %#v, error = %v", selection.Items, err)
	}
}

func TestChangedHistoryAndPlatformLifecycle(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	changed := strings.Replace(checkerManifest, "  current: \"1.0\"", "  current: \"2.0\"", 1)
	changed = strings.Replace(changed, "application:\n", "    - version: \"2.0\"\n      artifacts:\n        linux-amd64:\n          url: https://example.com/fixture-2.0.tar.gz\n          verification:\n            digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef\n            source: https://example.com/SHA256SUMS-2.0\napplication:\n", 1)
	selection, err := Changed(writeCheckerRegistry(t, changed), oldRoot)
	if err != nil || len(selection.Items) != 1 || selection.Items[0].Release.Version != "2.0" {
		t.Fatalf("history selection = %#v, error = %v", selection.Items, err)
	}

	dual := checkerDualPlatform()
	selection, err = Changed(writeCheckerRegistry(t, dual), writeCheckerRegistry(t, checkerManifest))
	if err != nil || len(selection.Items) != 1 || selection.Items[0].Platform.Arch != "arm64" {
		t.Fatalf("new platform selection = %#v, error = %v", selection.Items, err)
	}
	if _, err := Changed(writeCheckerRegistry(t, checkerManifest), writeCheckerRegistry(t, dual)); err == nil {
		t.Fatal("removed approved platform unexpectedly accepted")
	}
}

func TestStructuralRejectsSchemaV4(t *testing.T) {
	legacy := strings.Replace(checkerManifest, "schema: 5", "schema: 4", 1)
	if err := Structural(writeCheckerRegistry(t, legacy)); err == nil {
		t.Fatal("schema v4 unexpectedly accepted")
	}
}
