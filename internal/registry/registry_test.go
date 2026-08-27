package registry

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/manifest"
)

const testManifest = `schema: 4
id: blender
name: Blender
summary: 3D creation suite
homepage: https://www.blender.org/
categories: [game-development, graphics]
platforms:
  linux-amd64:
    revision: 1
    release:
      default-channel: stable
      channels:
        stable:
          current: "5.2.0"
      releases:
        - channel: stable
          version: "5.2.0"
          url: https://download.blender.org/release/Blender5.2/blender-5.2.0-linux-x64.tar.xz
          archive: tar.xz
          verification:
            algorithm: sha256
            digest: 96f6c181a30f4950607839dc84d42a354b250d8a0231b098b59b7bc69c351c48
            source: https://example.com/blender-checksums
    application: {executables: [{name: blender, path: blender}]}
    desktop: {enabled: true, categories: [Graphics], icon: null}
`

func withArm64(base string) string {
	arm := strings.Replace(base, "linux-amd64:", "linux-arm64:", 1)
	start := strings.Index(arm, "  linux-arm64:")
	return base + arm[start:]
}

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

func TestValidateTreeSupportsExactPlatformVariants(t *testing.T) {
	root := createRegistry(t)
	arm64 := withArm64(testManifest)
	armStart := strings.Index(arm64, "  linux-arm64:")
	arm64 = arm64[:armStart] + strings.ReplaceAll(arm64[armStart:], `"5.2.0"`, `"5.2.0-arm64"`)
	arm64 = arm64[:armStart] + strings.Replace(arm64[armStart:], "name: blender, path: blender", "name: blender-arm64, path: blender", 1)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(arm64), 0o644); err != nil {
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

func TestReleaseForPlatformResolvesChannelAndOpaqueVersion(t *testing.T) {
	root := createRegistry(t)
	content, err := os.ReadFile(filepath.Join(root, "apps", "blender", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(content), "      channels:\n        stable:\n          current: \"5.2.0\"", "      channels:\n        stable:\n          current: \"5.2.0\"\n        preview:\n          current: \"2.7.513\"", 1)
	mutated = strings.Replace(mutated, "    application: {executables:", "        - channel: preview\n          version: \"2.7.513\"\n          url: https://download.blender.org/release/Blender5.2/preview.tar.xz\n          archive: tar.xz\n          verification:\n            algorithm: sha256\n            digest: abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd\n            source: https://download.blender.org/release/Blender5.2/preview.sha256\n    application: {executables:", 1)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"preview", "2.7.513"} {
		item, err := catalog.ReleaseForPlatform("blender", "linux", "amd64", selector)
		if err != nil || item.Release.Version != "2.7.513" || item.Release.Channel != "preview" {
			t.Fatalf("selector %q = %#v, error = %v", selector, item, err)
		}
	}
	for _, selector := range []string{"missing", "stable@preview"} {
		if _, err := catalog.ReleaseForPlatform("blender", "linux", "amd64", selector); err == nil {
			t.Fatalf("unknown selector %q unexpectedly resolved", selector)
		}
	}
}

func TestReleaseSelectorsPreserveReleaseScopedNestedRecipes(t *testing.T) {
	root := createRegistry(t)
	path := filepath.Join(root, "apps", "blender", "manifest.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(content), "          archive: tar.xz\n          verification:", "          archive: tar.xz\n          nested-archive: {path: stable.zip, archive: zip}\n          verification:", 1)
	mutated = strings.Replace(mutated, "        - channel: stable", "        - channel: preview\n          version: \"2.0\"\n          url: https://example.com/preview.tar.xz\n          archive: tar.xz\n          nested-archive: {path: preview.zip, archive: zip}\n          verification:\n            algorithm: sha256\n            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n            source: https://example.com/preview.sha256\n        - channel: stable", 1)
	mutated = strings.Replace(mutated, "        stable:\n          current: \"5.2.0\"", "        stable:\n          current: \"5.2.0\"\n        preview:\n          current: \"2.0\"", 1)
	if err := os.WriteFile(path, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for selector, want := range map[string]string{"": "stable.zip", "stable": "stable.zip", "preview": "preview.zip", "2.0": "preview.zip"} {
		var item *manifest.Manifest
		if selector == "" {
			item, err = catalog.ManifestForPlatform("blender", "linux", "amd64")
		} else {
			item, err = catalog.ReleaseForPlatform("blender", "linux", "amd64", selector)
		}
		if err != nil {
			t.Fatalf("selector %q: %v", selector, err)
		}
		if item.Release.NestedArchive.Path != want {
			t.Fatalf("selector %q nested path = %q, want %q", selector, item.Release.NestedArchive.Path, want)
		}
	}
}

func TestValidateTreeRejectsPlatformLayoutViolations(t *testing.T) {
	tests := map[string]func(string) (string, string){
		"legacy manifest filename": func(root string) (string, string) {
			return filepath.Join(root, "apps", "blender", "linux-amd64.yaml"), testManifest
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
	t.Run("shared metadata is represented once", func(t *testing.T) {
		root := createRegistry(t)
		arm64 := withArm64(testManifest)
		if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(arm64), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err != nil {
			t.Fatalf("unified metadata unexpectedly rejected: %v", err)
		}
	})
	t.Run("duplicate names", func(t *testing.T) {
		root := createRegistry(t)
		if err := os.MkdirAll(filepath.Join(root, "apps", "other"), 0o755); err != nil {
			t.Fatal(err)
		}
		other := strings.Replace(testManifest, "id: blender", "id: other", 1)
		if err := os.WriteFile(filepath.Join(root, "apps", "other", "manifest.yaml"), []byte(other), 0o644); err != nil {
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

func TestValidateTreeUsesCheckedAtMetadataInsteadOfDirectoryTimestamp(t *testing.T) {
	root := createRegistry(t)
	checkedAt := time.Date(2026, 8, 27, 15, 4, 5, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(root, GenerationMetadataFile), []byte(`{"checked_at":"2026-08-27T15:04:05Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := checkedAt.Add(-72 * time.Hour)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.FetchedAt.Equal(checkedAt) {
		t.Fatalf("checked-at = %s, want %s", catalog.FetchedAt, checkedAt)
	}
}

func TestValidateTreeOldGenerationWithoutMetadataIsDisposable(t *testing.T) {
	catalog, err := ValidateTree(createRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.FetchedAt.IsZero() {
		t.Fatalf("missing metadata checked-at = %s, want zero", catalog.FetchedAt)
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
