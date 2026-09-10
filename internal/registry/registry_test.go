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

const testManifest = `schema: 5
id: blender
name: Blender
summary: 3D creation suite
homepage: https://www.blender.org/
categories: [game-development, graphics]
release:
  current: "5.2.0"
  archive: tar.xz
  verification:
    algorithm: sha256
  releases:
    - version: "5.2.0"
      artifacts:
        linux-amd64:
          url: https://download.blender.org/release/Blender5.2/blender-5.2.0-linux-x64.tar.xz
          verification:
            digest: 96f6c181a30f4950607839dc84d42a354b250d8a0231b098b59b7bc69c351c48
            source: https://example.com/blender-checksums
application:
  executable:
    name: blender
    path: blender
desktop:
  categories: [Graphics]
`

func withArm64(base string) string {
	arm := `        linux-arm64:
          url: https://download.blender.org/release/Blender5.2/blender-5.2.0-linux-arm64.tar.xz
          verification:
            digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
            source: https://example.com/blender-checksums
`
	return strings.Replace(base, "application:\n", arm+"application:\n", 1)
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

func writeTestRuntime(t *testing.T, root, id, version string) {
	t.Helper()
	directory := filepath.Join(root, "runtimes", id)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	value := `schema: 1
id: ` + id + `
kind: steam-linux-runtime
version: "` + version + `"
platform: linux-amd64
artifact:
  url: https://example.com/` + id + `.tar.xz
  archive: tar.xz
  verification:
    algorithm: sha256
    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    source: https://example.com/SHA256SUMS
interface: valve-v2-entry-point-v1
`
	if err := os.WriteFile(filepath.Join(directory, "manifest.yaml"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runtimeHistoryManifest() string {
	return `schema: 5
id: blender
name: Blender
summary: 3D creation suite
homepage: https://www.blender.org/
categories: [graphics]
release:
  default-channel: stable
  channels:
    stable:
      current: "2.0"
    beta:
      current: "3.0"
  archive: tar.xz
  verification:
    algorithm: sha256
  releases:
    - channel: stable
      version: "2.0"
      runtime:
        id: steam-linux-runtime-4
        version: "4.0"
      artifacts:
        linux-amd64:
          url: https://example.com/blender-2.tar.xz
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/checksums
    - channel: stable
      version: "1.0"
      artifacts:
        linux-amd64:
          url: https://example.com/blender-1.tar.xz
          verification:
            digest: 1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/checksums
    - channel: beta
      version: "3.0"
      runtime:
        id: steam-linux-runtime-3
        version: "3.0"
      artifacts:
        linux-amd64:
          url: https://example.com/blender-3.tar.xz
          verification:
            digest: 2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/checksums
application:
  executable:
    name: blender
    path: blender
    create-bin-link: false
`
}

func TestReleaseScopedRuntimesResolvePerSelectedRelease(t *testing.T) {
	root := createRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(runtimeHistoryManifest()), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestRuntime(t, root, "steam-linux-runtime-3", "3.0")
	writeTestRuntime(t, root, "steam-linux-runtime-4", "4.0")
	catalog, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for selector, want := range map[string]string{"": "steam-linux-runtime-4", "stable": "steam-linux-runtime-4", "1.0": "", "beta": "steam-linux-runtime-3"} {
		var item *manifest.Manifest
		if selector == "" {
			item, err = catalog.ManifestForPlatform("blender", "linux", "amd64")
		} else {
			item, err = catalog.ReleaseForPlatform("blender", "linux", "amd64", selector)
		}
		if err != nil {
			t.Fatalf("selector %q: %v", selector, err)
		}
		got := ""
		if item.RuntimeRef != nil {
			got = item.RuntimeRef.ID
		}
		if got != want {
			t.Fatalf("selector %q runtime = %q, want %q", selector, got, want)
		}
	}
	first, err := catalog.ReleaseForPlatform("blender", "linux", "amd64", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	firstFingerprint, err := first.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(runtimeHistoryManifest(), "id: steam-linux-runtime-4\n        version: \"4.0\"", "id: steam-linux-runtime-3\n        version: \"3.0\"", 1)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := changed.ReleaseForPlatform("blender", "linux", "amd64", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := second.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint == secondFingerprint {
		t.Fatal("runtime-only release change did not change fingerprint")
	}
}

func TestValidateTreeRejectsUnavailableReleaseRuntime(t *testing.T) {
	root := createRegistry(t)
	value := strings.Replace(runtimeHistoryManifest(), "steam-linux-runtime-4", "missing-runtime", 1)
	if err := os.WriteFile(filepath.Join(root, "apps", "blender", "manifest.yaml"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	writeTestRuntime(t, root, "steam-linux-runtime-3", "3.0")
	if _, err := ValidateTree(root); err == nil {
		t.Fatal("unavailable release runtime unexpectedly accepted")
	}
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
	if arm.Release.Version != "5.2.0" || arm.Application.Executables[0].Name != "blender" {
		t.Fatalf("arm64 manifest = %#v", arm)
	}
	if got := catalog.SearchForPlatform("3d", "linux", "arm64"); len(got) != 1 || got[0].ID != "blender" || got[0].Release.Version != "5.2.0" || got[0].Application.Executables[0].Name != "blender" {
		t.Fatalf("arm64 SearchForPlatform() = %#v", got)
	}
}

func TestReleaseForPlatformResolvesChannelAndOpaqueVersion(t *testing.T) {
	root := createRegistry(t)
	content, err := os.ReadFile(filepath.Join(root, "apps", "blender", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(content), "  current: \"5.2.0\"", "  default-channel: stable\n  channels:\n    stable:\n      current: \"5.2.0\"\n    preview:\n      current: \"2.7.513\"", 1)
	mutated = strings.Replace(mutated, "    - version: \"5.2.0\"", "    - channel: stable\n      version: \"5.2.0\"", 1)
	mutated = strings.Replace(mutated, "application:\n", "    - channel: preview\n      version: \"2.7.513\"\n      artifacts:\n        linux-amd64:\n          url: https://download.blender.org/release/Blender5.2/preview.tar.xz\n          verification:\n            digest: abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd\n            source: https://download.blender.org/release/Blender5.2/preview.sha256\napplication:\n", 1)
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
	mutated := strings.Replace(string(content), "    - version: \"5.2.0\"", "    - channel: stable\n      version: \"5.2.0\"\n      nested-archive: {path: stable.zip, archive: zip}", 1)
	mutated = strings.Replace(mutated, "application:\n", "    - channel: preview\n      version: \"2.0\"\n      nested-archive: {path: preview.zip, archive: zip}\n      artifacts:\n        linux-amd64:\n          url: https://example.com/preview.tar.xz\n          verification:\n            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n            source: https://example.com/preview.sha256\napplication:\n", 1)
	mutated = strings.Replace(mutated, "  current: \"5.2.0\"", "  default-channel: stable\n  channels:\n    stable:\n      current: \"5.2.0\"\n    preview:\n      current: \"2.0\"", 1)
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
