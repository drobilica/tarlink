package registry

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
  sha256: 96f6c181a30f4950607839dc84d42a354b250d8a0231b098b59b7bc69c351c48
  archive: tar.xz
application: {executable: blender}
desktop: {enabled: true, categories: [Graphics]}
`

func createRegistry(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"apps/blender", "policy", "index"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "apps/blender/manifest.yaml"), []byte(testManifest), 0o644); err != nil {
		t.Fatal(err)
	}
	policy := "schema: 1\nsources:\n  blender:\n    - https://download.blender.org/release/Blender5.2/\n"
	if err := os.WriteFile(filepath.Join(root, "policy/approved-sources.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	manifests, err := loadManifests(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := IndexBytes(GenerateIndex(manifests))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index/index.json"), index, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestValidateTreeAndSearch(t *testing.T) {
	catalog, err := ValidateTree(createRegistry(t))
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

func TestPolicyRejectsCanonicalizationEscape(t *testing.T) {
	catalog, err := ValidateTree(createRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := url.Parse("https://download.blender.org/release/Blender5.2/%2e%2e/other/artifact.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Policy.Allows("blender", candidate) {
		t.Fatal("noncanonical approved-source escape was accepted")
	}
}

func TestValidateTreeRejectsStaleIndexAndUnapprovedURL(t *testing.T) {
	t.Run("stale index", func(t *testing.T) {
		root := createRegistry(t)
		if err := os.WriteFile(filepath.Join(root, "index/index.json"), []byte(`{"schema":1,"apps":[]}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("stale index unexpectedly accepted")
		}
	})
	t.Run("unapproved URL", func(t *testing.T) {
		root := createRegistry(t)
		path := filepath.Join(root, "apps/blender/manifest.yaml")
		content, _ := os.ReadFile(path)
		content = []byte(strings.Replace(string(content), "download.blender.org", "evil.example", 1))
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateTree(root); err == nil {
			t.Fatal("unapproved URL unexpectedly accepted")
		}
	})
}

func TestValidateTreeRejectsSymlink(t *testing.T) {
	root := createRegistry(t)
	if err := os.Symlink("manifest.yaml", filepath.Join(root, "apps/blender/extra")); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateTree(root); err == nil {
		t.Fatal("registry symlink unexpectedly accepted")
	}
}

func TestRegistryWorkingTreeCompatibility(t *testing.T) {
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
		t.Fatalf("registry working tree is incompatible with the client: %v", err)
	}
	if catalog.Manifests["blender"] == nil {
		t.Fatal("reviewed Blender manifest is missing")
	}
}
