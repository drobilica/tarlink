package manifest

import (
	"strings"
	"testing"
)

const validManifest = `schema: 5
id: helm
name: Helm
summary: Package manager
homepage: https://helm.sh/
categories: [development, utilities]
release:
  current: 4.2.4
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: 4.2.4
      artifacts:
        linux-amd64:
          url: https://example.invalid/helm-amd64.tar.gz
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/helm-amd64.sha256
        linux-arm64:
          url: https://example.invalid/helm-arm64.tar.gz
          verification:
            digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
            source: https://example.invalid/helm-arm64.sha256
application:
  executable:
    name: helm
    paths:
      linux-amd64: linux-amd64/helm
      linux-arm64: linux-arm64/helm
`

func parsePackage(t *testing.T, value, platform string) *ResolvedPackage {
	t.Helper()
	document, err := ParseBytes([]byte(value))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	result, err := document.ResolvePackage(platform)
	if err != nil {
		t.Fatalf("ResolvePackage() error = %v", err)
	}
	return result
}

func TestParseAndResolveV5(t *testing.T) {
	amd64 := parsePackage(t, validManifest, PlatformLinuxAMD64)
	if amd64.Schema != SchemaV5 || amd64.Release.Version != "4.2.4" || amd64.Release.Archive != "tar.gz" {
		t.Fatalf("resolved = %#v", amd64)
	}
	if amd64.Application.Executables[0].Path != "linux-amd64/helm" || !strings.HasPrefix(amd64.Fingerprint, "sha256:") {
		t.Fatalf("resolved = %#v", amd64)
	}
	arm64 := parsePackage(t, validManifest, PlatformLinuxARM64)
	if arm64.Application.Executables[0].Path != "linux-arm64/helm" {
		t.Fatalf("arm path = %#v", arm64.Application.Executables)
	}
}

func TestSingleChannelIsImplicit(t *testing.T) {
	document, err := ParseBytes([]byte(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	if len(document.Platforms) != 2 {
		t.Fatalf("platforms = %#v", document.Platforms)
	}
	for _, item := range document.Platforms {
		if item.ReleaseHistory.DefaultChannel != "stable" || len(item.ReleaseHistory.Channels) != 1 {
			t.Fatalf("history = %#v", item.ReleaseHistory)
		}
	}
}

func TestMultiChannelHistory(t *testing.T) {
	value := strings.Replace(validManifest, "  current: 4.2.4\n", "  default-channel: stable\n  channels:\n    stable:\n      current: 4.2.4\n    nightly:\n      current: 4.2.5\n", 1)
	value = strings.Replace(value, "    - version: 4.2.4", "    - channel: stable\n      version: 4.2.4", 1)
	value = strings.Replace(value, "      artifacts:\n", "      artifacts:\n", 1)
	value = strings.Replace(value, "application:\n", "    - channel: nightly\n      version: 4.2.5\n      artifacts:\n        linux-amd64:\n          url: https://example.invalid/nightly-amd64.tar.gz\n          verification:\n            digest: 1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n            source: https://example.invalid/nightly-amd64.sha256\n        linux-arm64:\n          url: https://example.invalid/nightly-arm64.tar.gz\n          verification:\n            digest: 2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n            source: https://example.invalid/nightly-arm64.sha256\napplication:\n", 1)
	document, err := ParseBytes([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	if document.Platforms[PlatformLinuxAMD64].ReleaseHistory.Channels["nightly"].Current != "4.2.5" {
		t.Fatalf("history = %#v", document.Platforms[PlatformLinuxAMD64].ReleaseHistory)
	}
}

func TestRejectsLegacyAndMalformedForms(t *testing.T) {
	tests := map[string]string{
		"v4":                    "schema: 4\n",
		"platforms":             strings.Replace(validManifest, "release:\n", "platforms:\n  linux-amd64: {}\nrelease:\n", 1),
		"empty artifacts":       strings.Replace(validManifest, "        linux-amd64:\n", "        linux-amd64: {}\n", 1),
		"archive override":      strings.Replace(validManifest, "          url:", "          archive: zip\n          url:", 1),
		"unsupported archive":   strings.Replace(validManifest, "  archive: tar.gz", "  archive: 7z", 1),
		"bad digest":            strings.Replace(validManifest, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "xyz", 1),
		"both executable forms": strings.Replace(validManifest, "application:\n", "application:\n  executables:\n    - path: helm\n", 1),
		"unknown field":         strings.Replace(validManifest, "schema: 5\n", "schema: 5\nunknown: true\n", 1),
		"anchor":                strings.Replace(validManifest, "schema: 5\n", "schema: &schema 5\n", 1),
		"alias":                 strings.Replace(validManifest, "schema: 5\n", "schema: &schema 5\nid: *schema\n", 1),
		"merge key":             strings.Replace(validManifest, "release:\n", "release:\n  <<: {archive: tar.gz}\n", 1),
		"second document":       validManifest + "\n---\nschema: 5\n",
		"unsupported tag":       strings.Replace(validManifest, "schema: 5", "schema: !!float 5", 1),
		"invalid algorithm":     strings.Replace(validManifest, "algorithm: sha256", "algorithm: md5", 1),
		"missing arm path":      strings.Replace(validManifest, "linux-arm64: linux-arm64/helm\n", "linux-arm64: ''\n", 1),
		"duplicate key":         strings.Replace(validManifest, "schema: 5\n", "schema: 5\nschema: 5\n", 1),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBytes([]byte(value)); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestMixedArchiveAndNestedArchive(t *testing.T) {
	value := strings.Replace(validManifest, "  archive: tar.gz\n", "", 1)
	value = strings.Replace(value, "        linux-amd64:\n", "        linux-amd64:\n          archive: appimage\n", 1)
	value = strings.Replace(value, "        linux-arm64:\n", "        linux-arm64:\n          archive: tar.xz\n", 1)
	value = strings.Replace(value, "linux-amd64/helm", "appimage", 1)
	if _, err := ParseBytes([]byte(value)); err != nil {
		t.Fatalf("mixed archive form: %v", err)
	}
	value = strings.Replace(validManifest, "    - version: 4.2.4", "    - version: 4.2.4\n      nested-archive:\n        path: inner.tar.gz\n        archive: tar.gz", 1)
	value = strings.Replace(value, "  archive: tar.gz", "  archive: appimage", 1)
	if _, err := ParseBytes([]byte(value)); err == nil {
		t.Fatal("AppImage nested archive accepted")
	}
}

func TestFingerprintIgnoresMetadataAndTracksMaterialization(t *testing.T) {
	base := parsePackage(t, validManifest, PlatformLinuxAMD64).Fingerprint
	mutate := func(old, new string) string { return strings.Replace(validManifest, old, new, 1) }
	for _, value := range []string{mutate("summary: Package manager", "summary: Other"), mutate("homepage: https://helm.sh/", "homepage: https://example.invalid/home"), mutate("source: https://example.invalid/helm-amd64.sha256", "source: https://example.invalid/release")} {
		if got := parsePackage(t, value, PlatformLinuxAMD64).Fingerprint; got != base {
			t.Fatalf("metadata changed fingerprint: %s", got)
		}
	}
	for _, value := range []string{mutate("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), mutate("  archive: tar.gz", "  archive: zip"), mutate("linux-amd64/helm", "bin/helm")} {
		if got := parsePackage(t, value, PlatformLinuxAMD64).Fingerprint; got == base {
			t.Fatal("material change did not change fingerprint")
		}
	}
}

func TestFingerprintUsesEffectiveDesktopInputs(t *testing.T) {
	base := parsePackage(t, validManifest, PlatformLinuxAMD64).Manifest
	withoutDesktop := base
	withoutDesktop.Name = "Different name"
	baseFingerprint, err := base.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	withoutDesktopFingerprint, err := withoutDesktop.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if withoutDesktopFingerprint != baseFingerprint {
		t.Fatalf("name changed a non-desktop package fingerprint: %q != %q", withoutDesktopFingerprint, baseFingerprint)
	}

	withDesktop := base
	withDesktop.Desktop.Enabled = true
	withDesktop.Desktop.Categories = []string{"Utility"}
	withDesktopFingerprint, err := withDesktop.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	changedDesktopName := withDesktop
	changedDesktopName.Name = "Different name"
	changedNameFingerprint, err := changedDesktopName.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changedNameFingerprint == withDesktopFingerprint {
		t.Fatal("desktop application name did not change fingerprint")
	}

	withExplicitDesktopExecutable := withDesktop
	withExplicitDesktopExecutable.Desktop.Executable = withDesktop.Application.Executables[0].Name
	explicitFingerprint, err := withExplicitDesktopExecutable.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if explicitFingerprint != withDesktopFingerprint {
		t.Fatalf("omitted and explicit sole desktop executable differ: %q != %q", explicitFingerprint, withDesktopFingerprint)
	}

	withMultipleExecutables := withDesktop
	withMultipleExecutables.Application.Executables = append(append([]Executable(nil), withDesktop.Application.Executables...), Executable{Name: "other", Path: "bin/other"})
	withMultipleExecutables.Desktop.Executable = withMultipleExecutables.Application.Executables[0].Name
	firstSelectionFingerprint, err := withMultipleExecutables.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	withMultipleExecutables.Desktop.Executable = "other"
	secondSelectionFingerprint, err := withMultipleExecutables.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if firstSelectionFingerprint == secondSelectionFingerprint {
		t.Fatal("changing multi-executable desktop selection did not change fingerprint")
	}
}
