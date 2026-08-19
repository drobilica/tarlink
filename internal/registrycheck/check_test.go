package registrycheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
application: {executable: fixture}
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
