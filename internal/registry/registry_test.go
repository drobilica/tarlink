package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testManifest = `schema: 1
id: blender
name: Blender
summary: 3D creation suite
homepage: https://www.blender.org/
categories: [game-development, graphics]
platform: {os: linux, arch: amd64}
release:
  version: "5.2.0"
  url: https://download.blender.org/release/Blender5.2/blender-5.2.0-linux-x64.tar.xz
  archive: tar.xz
  verification:
    algorithm: sha256
    digest: 96f6c181a30f4950607839dc84d42a354b250d8a0231b098b59b7bc69c351c48
    source: https://download.blender.org/release/Blender5.2/blender-5.2.0.sha256
application: {executable: blender}
desktop: {enabled: true, categories: [Graphics]}
`

func createRegistry(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "blender"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateTreeAndSearch(t *testing.T) {
	root := createRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("registry documentation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatalf("ValidateTree() error = %v", err)
	}
	if got := catalog.Search("3d"); len(got) != 1 || got[0].ID != "blender" {
		t.Fatalf("Search() = %#v", got)
	}
	if got := catalog.Search("emulation"); len(got) != 0 {
		t.Fatalf("Search() = %#v", got)
	}
}

func TestValidateTreeRejectsInvalidApplicationData(t *testing.T) {
	t.Run("manifest URL", func(t *testing.T) {
		root := createRegistry(t)
		path := filepath.Join(root, "apps", "blender", "manifest.yaml")
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content = []byte(strings.Replace(string(content), "https://download.blender.org/release/Blender5.2/blender-5.2.0-linux-x64.tar.xz", "http://example.test/blender.tar.xz", 1))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("insecure manifest URL unexpectedly accepted")
		}
	})
	t.Run("extra file", func(t *testing.T) {
		root := createRegistry(t)
		if err := os.WriteFile(filepath.Join(root, "apps", "blender", "notes"), []byte("unexpected"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("extra application file unexpectedly accepted")
		}
	})
}

func TestValidateTreeRejectsSymlink(t *testing.T) {
	root := createRegistry(t)
	if err := os.Symlink("blender", filepath.Join(root, "apps", "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTree(root); err == nil {
		t.Fatal("registry symlink unexpectedly accepted")
	}
}

func TestOpenRejectsEscapingCurrentPointer(t *testing.T) {
	cache := t.TempDir()
	if err := os.Symlink("../outside", filepath.Join(cache, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cache); err == nil {
		t.Fatal("escaping current pointer unexpectedly accepted")
	}
	if err := os.Remove(filepath.Join(cache, "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(cache); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing registry error = %v", err)
	}
}

func TestCatalogStaleness(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	catalog := &Catalog{FetchedAt: now.Add(-23 * time.Hour)}
	if catalog.Stale(now, DefaultMaxAge) {
		t.Fatal("23-hour-old registry reported stale")
	}
	catalog.FetchedAt = now.Add(-DefaultMaxAge)
	if !catalog.Stale(now, DefaultMaxAge) {
		t.Fatal("24-hour-old registry reported fresh")
	}
}

func TestRegistryWorkingTreeIntegration(t *testing.T) {
	root := os.Getenv("TARLINK_REGISTRY_WORKTREE")
	if root == "" {
		t.Skip("set TARLINK_REGISTRY_WORKTREE for cross-repository acceptance")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(absolute)
	if err != nil {
		t.Fatalf("registry working tree failed client validation: %v", err)
	}
	for _, id := range []string{"blender", "godot"} {
		if catalog.Manifests[id] == nil {
			t.Fatalf("reviewed %s manifest is missing", id)
		}
	}
}
