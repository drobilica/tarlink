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
application: {executables: [{name: blender, path: blender}]}
desktop: {enabled: true, categories: [Graphics]}
`

func createRegistry(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps", "blender"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "linux-amd64.yaml"), []byte(testManifest), 0o644); err != nil {
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
	if got := catalog.SearchForPlatform("3d", "linux", "amd64"); len(got) != 1 || got[0].ID != "blender" {
		t.Fatalf("SearchForPlatform() = %#v", got)
	}
	if got := catalog.SearchForPlatform("emulation", "linux", "amd64"); len(got) != 0 {
		t.Fatalf("SearchForPlatform() = %#v", got)
	}
}

func TestValidateTreeRejectsInvalidApplicationData(t *testing.T) {
	t.Run("manifest URL", func(t *testing.T) {
		root := createRegistry(t)
		path := filepath.Join(root, "apps", "blender", "linux-amd64.yaml")
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

func TestValidateTreeSupportsExactPlatformVariants(t *testing.T) {
	root := createRegistry(t)
	arm64 := strings.Replace(testManifest, "arch: amd64", "arch: arm64", 1)
	arm64 = strings.Replace(arm64, `version: "5.2.0"`, `version: "5.2.0-arm64"`, 1)
	arm64 = strings.Replace(arm64, "name: blender, path: blender", "name: blender-arm64, path: blender", 1)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "linux-arm64.yaml"), []byte(arm64), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatalf("ValidateTree() error = %v", err)
	}
	amd64, err := catalog.ManifestForPlatform("blender", "linux", "amd64")
	if err != nil {
		t.Fatalf("amd64 lookup error = %v", err)
	}
	if amd64.Release.Version != "5.2.0" || amd64.Application.Executables[0].Name != "blender" {
		t.Fatalf("amd64 manifest = %#v", amd64)
	}
	arm, err := catalog.ManifestForPlatform("blender", "linux", "arm64")
	if err != nil {
		t.Fatalf("arm64 lookup error = %v", err)
	}
	if arm.Release.Version != "5.2.0-arm64" || arm.Application.Executables[0].Name != "blender-arm64" {
		t.Fatalf("arm64 manifest = %#v", arm)
	}
	if got := catalog.SearchForPlatform("3d", "linux", "arm64"); len(got) != 1 || got[0].ID != "blender" || got[0].Release.Version != "5.2.0-arm64" || got[0].Application.Executables[0].Name != "blender-arm64" {
		t.Fatalf("arm64 SearchForPlatform() = %#v", got)
	}
}

func TestValidateTreeRejectsPlatformLayoutViolations(t *testing.T) {
	tests := map[string]func(string) (string, string){
		"legacy manifest filename": func(root string) (string, string) {
			return filepath.Join(root, "apps", "blender", "manifest.yaml"), testManifest
		},
		"mismatched filename": func(root string) (string, string) {
			return filepath.Join(root, "apps", "blender", "linux-arm64.yaml"), testManifest
		},
		"unexpected platform filename": func(root string) (string, string) {
			return filepath.Join(root, "apps", "blender", "darwin-amd64.yaml"), testManifest
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "apps", "blender"), 0o755); err != nil {
				t.Fatal(err)
			}
			path, content := setup(root)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ValidateTree(root); err == nil {
				t.Fatal("invalid platform layout unexpectedly accepted")
			}
		})
	}
}

func TestValidateTreeRejectsInconsistentVariantsAndDuplicateNames(t *testing.T) {
	t.Run("inconsistent shared metadata", func(t *testing.T) {
		root := createRegistry(t)
		arm64 := strings.Replace(testManifest, "arch: amd64", "arch: arm64", 1)
		arm64 = strings.Replace(arm64, "summary: 3D creation suite", "summary: Different summary", 1)
		if err := os.WriteFile(filepath.Join(root, "apps", "blender", "linux-arm64.yaml"), []byte(arm64), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("inconsistent variants unexpectedly accepted")
		}
	})
	t.Run("duplicate names", func(t *testing.T) {
		root := createRegistry(t)
		if err := os.MkdirAll(filepath.Join(root, "apps", "other"), 0o755); err != nil {
			t.Fatal(err)
		}
		other := strings.Replace(testManifest, "id: blender", "id: other", 1)
		if err := os.WriteFile(filepath.Join(root, "apps", "other", "linux-amd64.yaml"), []byte(other), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("duplicate names unexpectedly accepted")
		}
	})
}

func TestManifestForPlatformReportsTypedUnavailable(t *testing.T) {
	root := createRegistry(t)
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.ManifestForPlatform("blender", "linux", "arm64")
	if !errors.Is(err, ErrUnavailableForPlatform) {
		t.Fatalf("ManifestForPlatform() error = %v", err)
	}
	if !strings.Contains(err.Error(), "Blender is not available for linux/arm64") {
		t.Fatalf("ManifestForPlatform() error = %v", err)
	}
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
		if catalog.Variants[id] == nil {
			t.Fatalf("reviewed %s manifest is missing", id)
		}
	}
}
