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

const checkerManifest = `schema: 1
id: fixture
name: Fixture
summary: Fixture application
homepage: https://example.com/
categories: [utilities]
platform: {os: linux, arch: amd64}
release:
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
	return &manifest.Manifest{
		Schema: 1, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: manifest.Release{Version: "1.0", URL: server.URL, Verification: manifest.Verification{
			Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.URL + "/SHA256SUMS",
		}, Archive: "tar.gz"},
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

	newRoot = writeCheckerRegistry(t, strings.Replace(checkerManifest, "version: \"1.0\"", "version: \"2.0\"", 1))
	selection, err = Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].ID != "fixture" {
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
