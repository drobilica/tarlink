package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
)

const onboardingValidManifest = `schema: 5
id: foo
name: Foo
summary: A portable application
homepage: https://example.invalid/foo
categories: [utilities]
release:
  current: 1.2.3
  archive: zip
  verification:
    algorithm: sha256
  releases:
    - version: 1.2.3
      artifacts:
        linux-amd64:
          url: https://example.invalid/foo.zip
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/foo.zip
application:
  executable:
    name: foo
    path: foo
`

func writeOnboardingManifest(t *testing.T, path string, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectDirectoryIsBoundedDeterministicAndIgnoresSymlinkDirectories(t *testing.T) {
	root := t.TempDir()
	writeOnboardingManifest(t, filepath.Join(root, "apps", "zulu", "manifest.yaml"), onboardingValidManifest)
	writeOnboardingManifest(t, filepath.Join(root, "apps", "alpha", "manifest.yaml"), strings.Replace(onboardingValidManifest, "id: foo", "id: alpha", 1))
	writeOnboardingManifest(t, filepath.Join(root, "manifest.yaml"), strings.Replace(onboardingValidManifest, "id: foo", "id: root", 1))
	writeOnboardingManifest(t, filepath.Join(root, "apps", "too-deep", "nested", "manifest.yaml"), strings.Replace(onboardingValidManifest, "id: foo", "id: deep", 1))
	writeOnboardingManifest(t, filepath.Join(root, "apps", "invalid", "manifest.yaml"), "schema: 5\nid: [not valid\n")
	ignoredRoot := t.TempDir()
	writeOnboardingManifest(t, filepath.Join(ignoredRoot, "manifest.yaml"), onboardingValidManifest)
	if err := os.Symlink(ignoredRoot, filepath.Join(root, "apps", "symlinked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	first, err := inspectDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := inspectDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("directory inspection is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if first.Manifests != 4 || first.Valid != 3 || first.Invalid != 1 || first.Warnings != 0 {
		t.Fatalf("summary = %#v", first)
	}
	wantPaths := []string{
		"apps/alpha/manifest.yaml",
		"apps/invalid/manifest.yaml",
		"apps/zulu/manifest.yaml",
		"manifest.yaml",
	}
	var gotPaths []string
	for _, result := range first.Results {
		gotPaths = append(gotPaths, result.Path)
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("manifest order = %#v, want %#v", gotPaths, wantPaths)
	}
	for _, result := range first.Results {
		if result.Path == "apps/invalid/manifest.yaml" {
			if result.Valid || result.Error == "" {
				t.Fatalf("invalid result = %#v", result)
			}
		} else if !result.Valid {
			t.Fatalf("valid result = %#v", result)
		}
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"manifests":4`) || !strings.Contains(string(encoded), `"results"`) {
		t.Fatalf("directory JSON = %s", encoded)
	}
}

func TestInspectManifestUsesSchemaV5Parser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.yaml")
	writeOnboardingManifest(t, path, onboardingValidManifest)
	result := inspectManifest(path, "manifest.yaml")
	if !result.Valid || result.ID != "foo" || result.Error != "" {
		t.Fatalf("inspection = %#v", result)
	}
	writeOnboardingManifest(t, path, strings.Replace(onboardingValidManifest, "schema: 5", "schema: 4", 1))
	result = inspectManifest(path, "manifest.yaml")
	if result.Valid || result.Error == "" {
		t.Fatalf("legacy manifest inspection = %#v", result)
	}
}

func TestCompleteRegistryCandidateProducesParseableSchemaV5Manifest(t *testing.T) {
	binLink := true
	candidate := RegistryCandidate{
		Repository: "owner/foo",
		Release:    "v1.2.3",
		Asset:      "foo-linux-amd64.zip",
		URL:        "https://github.com/owner/foo/releases/download/v1.2.3/foo-linux-amd64.zip",
		Platform:   manifest.PlatformLinuxAMD64,
		Archive:    "zip",
		SHA256:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Executable: "foo",
		Icon:       "icons/foo.png",
	}
	result, err := CompleteRegistryCandidate(candidate, RegistryAddOptions{
		NonInteractive: true,
		Categories:     []string{"games"},
		CreateBinLink:  &binLink,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ready" || len(result.YAML) == 0 || result.Manifest == nil {
		t.Fatalf("result = %#v", result)
	}
	document, err := manifest.Parse(strings.NewReader(string(result.YAML)))
	if err != nil {
		t.Fatalf("generated YAML is not schema-v5: %v\n%s", err, result.YAML)
	}
	if document.Schema != manifest.SchemaV5 || document.ID != "foo" || document.Release.Current != "v1.2.3" {
		t.Fatalf("document = %#v", document)
	}
	if document.Release.Releases[0].Artifacts[manifest.PlatformLinuxAMD64].Verification.Digest != candidate.SHA256 {
		t.Fatalf("digest = %#v", document.Release.Releases[0].Artifacts)
	}
	if document.Application.Executable == nil || document.Application.Executable.Path != "foo" {
		t.Fatalf("executable = %#v", document.Application.Executable)
	}
	if document.Desktop == nil || document.Desktop.Icon == nil || document.Desktop.Icon.Path != candidate.Icon {
		t.Fatalf("desktop = %#v", document.Desktop)
	}
}

func TestCompleteRegistryCandidateNonInteractiveReportsSemanticNeedsInput(t *testing.T) {
	result, err := CompleteRegistryCandidate(RegistryCandidate{
		Repository: "owner/foo", Release: "v1.2.3", URL: "https://example.invalid/foo.zip",
		Platform: manifest.PlatformLinuxAMD64, Archive: "zip", SHA256: strings.Repeat("a", 64), Executable: "foo",
	}, RegistryAddOptions{NonInteractive: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs-input" || len(result.Required) != 1 || result.Required[0] != (RegistryRequiredInput{Field: "categories", Reason: "semantic_category_required"}) {
		t.Fatalf("needs-input result = %#v", result)
	}
}

func TestCompleteRegistryCandidateNonInteractiveReportsBinLinkPolicy(t *testing.T) {
	result, err := CompleteRegistryCandidate(RegistryCandidate{
		Repository: "owner/foo", Release: "v1.2.3", URL: "https://example.invalid/foo.zip",
		Platform: manifest.PlatformLinuxAMD64, Archive: "zip", SHA256: strings.Repeat("b", 64), Executable: "foo",
	}, RegistryAddOptions{NonInteractive: true, Categories: []string{"games"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "needs-input" || len(result.Required) != 1 || result.Required[0] != (RegistryRequiredInput{Field: "create-bin-link", Reason: "semantic_bin_link_policy_required"}) {
		t.Fatalf("needs-input result = %#v", result)
	}
}
