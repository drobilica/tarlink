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

func TestRuntimeReferencesBelongToReleaseDefinitions(t *testing.T) {
	value := strings.Replace(validManifest, "    - version: 4.2.4\n", "    - version: 4.2.4\n      runtime:\n        id: steam-linux-runtime-4\n        version: 4.0.1\n", 1)
	document, err := ParseBytes([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	release := document.Platforms[PlatformLinuxAMD64].ReleaseHistory.Releases[0]
	if release.RuntimeRef == nil || release.RuntimeRef.ID != "steam-linux-runtime-4" {
		t.Fatalf("release runtime = %#v", release.RuntimeRef)
	}
	resolved, err := document.ResolvePlatform(PlatformLinuxAMD64)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RuntimeRef == nil || resolved.RuntimeRef.ID != "steam-linux-runtime-4" {
		t.Fatalf("resolved runtime = %#v", resolved.RuntimeRef)
	}
}

func TestRejectsInvalidReleaseRuntimeReference(t *testing.T) {
	base := strings.Replace(validManifest, "    - version: 4.2.4\n", "    - version: 4.2.4\n      runtime:\n        id: steam-linux-runtime-4\n        version: 4.0.1\n", 1)
	for name, value := range map[string]string{
		"id":      strings.Replace(base, "id: steam-linux-runtime-4", "id: Invalid_Runtime", 1),
		"version": strings.Replace(base, "version: 4.0.1", "version: ''", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBytes([]byte(value)); err == nil {
				t.Fatal("invalid release runtime reference unexpectedly accepted")
			}
		})
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
		"document runtime":      strings.Replace(validManifest, "release:\n", "runtime: {id: steam-linux-runtime-4, version: 4.0.1}\nrelease:\n", 1),
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
	withDesktop.Desktop.Icon = DesktopIcon{Path: "icon.png"}
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
	withRemoteSVG := withDesktop
	withRemoteSVG.Desktop.Icon = DesktopIcon{URL: "https://example.com/icon.svg", SHA256: strings.Repeat("a", 64)}
	remoteFingerprint, err := withRemoteSVG.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	withRemoteSVG.Desktop.Icon.SHA256 = strings.Repeat("b", 64)
	changedIconFingerprint, err := withRemoteSVG.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if remoteFingerprint == changedIconFingerprint {
		t.Fatal("changing the pinned remote SVG bytes did not change the package fingerprint")
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

func TestDesktopIntegrationAllowsMissingIcon(t *testing.T) {
	value := validManifest + "desktop:\n  categories: [Utility]\n"
	if _, err := ParseBytes([]byte(value)); err != nil {
		t.Fatalf("desktop manifest without icon rejected: %v", err)
	}

	withIcon := value + ""
	withIcon = strings.Replace(withIcon, "categories: [Utility]\n", "categories: [Utility]\n  icon:\n    path: icon.png\n", 1)
	if _, err := ParseBytes([]byte(withIcon)); err != nil {
		t.Fatalf("desktop manifest with icon rejected: %v", err)
	}

	base := parsePackage(t, validManifest, PlatformLinuxAMD64).Manifest
	base.Desktop = Desktop{Icon: DesktopIcon{Path: "icon.png"}}
	if err := validateApplicationRelease(base, base.Release); err == nil {
		t.Fatal("icon without desktop integration accepted")
	}
}

func TestRemoteDesktopIconAcceptsPNGAndSVGOnly(t *testing.T) {
	base := parsePackage(t, validManifest, PlatformLinuxAMD64).Manifest
	base.Desktop = Desktop{Enabled: true, Categories: []string{"Utility"}, Icon: DesktopIcon{
		URL: "https://example.com/icon.svg", SHA256: strings.Repeat("a", 64),
	}}
	if err := base.Validate(); err != nil {
		t.Fatalf("SVG URL rejected: %v", err)
	}
	if got := base.Desktop.Icon.Extension(); got != ".svg" {
		t.Fatalf("SVG extension = %q", got)
	}
	base.Desktop.Icon.URL = "https://example.com/icon.png"
	if err := base.Validate(); err != nil {
		t.Fatalf("PNG URL rejected: %v", err)
	}
	base.Desktop.Icon.URL = "https://example.com/icon.svg?download=1"
	if err := base.Validate(); err != nil {
		t.Fatalf("SVG URL with query rejected: %v", err)
	}
	base.Desktop.Icon.URL = "https://example.com/icon.ico"
	if err := base.Validate(); err == nil {
		t.Fatal("unsupported icon URL extension accepted")
	}
}

func TestEnabledDefaultsToActive(t *testing.T) {
	resolved := parsePackage(t, validManifest, PlatformLinuxAMD64)
	if !resolved.WantsEnabled() {
		t.Fatal("omitted enabled must keep the application active")
	}
}

func TestExplicitEnabledTrueKeepsApplicationActive(t *testing.T) {
	value := strings.Replace(validManifest, "categories: [development, utilities]", "categories: [development, utilities]\nenabled: true", 1)
	resolved := parsePackage(t, value, PlatformLinuxAMD64)
	if !resolved.WantsEnabled() {
		t.Fatal("explicit enabled true must keep the application active")
	}
}

func TestExplicitEnabledFalseDisablesApplication(t *testing.T) {
	value := strings.Replace(validManifest, "categories: [development, utilities]", "categories: [development, utilities]\nenabled: false", 1)
	resolved := parsePackage(t, value, PlatformLinuxAMD64)
	if resolved.WantsEnabled() {
		t.Fatal("explicit enabled false must disable the application")
	}
}

// twoReleaseManifest retains 4.2.3 next to the current 4.2.4; each release
// declares its own executable and the shared application declaration would
// otherwise apply to releases without one.
const twoReleaseManifest = `schema: 5
id: helm
name: Helm
summary: Package manager
homepage: https://helm.sh/
categories: [development, utilities]
release:
  current: "4.2.4"
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: "4.2.4"
      executable:
        name: newtool
        path: newtool
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
    - version: "4.2.3"
      executable:
        name: oldtool
        path: oldtool
      artifacts:
        linux-amd64:
          url: https://example.invalid/helm-4.2.3-amd64.tar.gz
          verification:
            digest: 1123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/helm-4.2.3-amd64.sha256
        linux-arm64:
          url: https://example.invalid/helm-4.2.3-arm64.tar.gz
          verification:
            digest: 2123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/helm-4.2.3-arm64.sha256
application:
  executable:
    name: shared
    paths:
      linux-amd64: shared-amd64
      linux-arm64: shared-arm64
`

// retainedFingerprint returns the resolved-package fingerprint of the second
// (retained) release in twoReleaseManifest for linux-amd64.
func retainedFingerprint(t *testing.T, value string) string {
	t.Helper()
	document, err := ParseBytes([]byte(value))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	resolved, err := document.ResolvePlatform(PlatformLinuxAMD64)
	if err != nil {
		t.Fatalf("ResolvePlatform() error = %v", err)
	}
	if len(resolved.ReleaseHistory.Releases) < 2 {
		t.Fatalf("expected a retained release, got %#v", resolved.ReleaseHistory.Releases)
	}
	projection := resolved.SelectRelease(resolved.ReleaseHistory.Releases[1])
	fingerprint, err := projection.ResolvedPackageFingerprint()
	if err != nil {
		t.Fatalf("ResolvedPackageFingerprint() error = %v", err)
	}
	return fingerprint
}

func TestReleaseExecutableOverrideSingle(t *testing.T) {
	value := strings.Replace(validManifest, "    - version: 4.2.4\n", "    - version: 4.2.4\n      executable:\n        path: bin/renamed\n", 1)
	amd64 := parsePackage(t, value, PlatformLinuxAMD64)
	executables := amd64.Application.Executables
	if len(executables) != 1 || executables[0].Name != "renamed" || executables[0].Path != "bin/renamed" {
		t.Fatalf("release override not resolved: %#v", executables)
	}
	if amd64.Release.Application == nil {
		t.Fatal("release override was not retained on the selected release")
	}
}

func TestReleaseExecutablesOverrideList(t *testing.T) {
	value := strings.Replace(validManifest, "    - version: 4.2.4\n", "    - version: 4.2.4\n      executables:\n        - path: bin/one\n        - name: two\n          path: bin/two-bin\n", 1)
	amd64 := parsePackage(t, value, PlatformLinuxAMD64)
	executables := amd64.Application.Executables
	if len(executables) != 2 {
		t.Fatalf("release executables = %#v", executables)
	}
	if executables[0].Name != "one" || executables[0].Path != "bin/one" {
		t.Fatalf("first executable = %#v", executables[0])
	}
	if executables[1].Name != "two" || executables[1].Path != "bin/two-bin" {
		t.Fatalf("second executable = %#v", executables[1])
	}
}

func TestRejectsReleaseOverrideForms(t *testing.T) {
	tests := map[string]string{
		"both executable and executables": "    - version: 4.2.4\n      executable:\n        path: bin/a\n      executables:\n        - path: bin/b\n",
		"paths lacks artifact platform":   "    - version: 4.2.4\n      executable:\n        paths:\n          linux-arm64: bin/arm\n",
		"empty executables sequence":      "    - version: 4.2.4\n      executables: []\n",
	}
	for name, insertion := range tests {
		t.Run(name, func(t *testing.T) {
			value := strings.Replace(validManifest, "    - version: 4.2.4\n", insertion, 1)
			if _, err := ParseBytes([]byte(value)); err == nil {
				t.Fatal("invalid release executable override accepted")
			}
		})
	}
}

func TestValidateHistoryUsesReleaseEffectiveExecutables(t *testing.T) {
	// The retained release must expose the desktop executable for the retained
	// release to validate; the shared declaration alone is not consulted.
	matching := strings.Replace(twoReleaseManifest, "        name: oldtool\n", "        name: newtool\n", 1)
	withDesktop := strings.Replace(matching, "application:\n", "desktop:\n  categories: [Utility]\n  executable: newtool\napplication:\n", 1)
	document, err := ParseBytes([]byte(withDesktop))
	if err != nil {
		t.Fatalf("manifest with per-release executables rejected: %v", err)
	}
	resolved, err := document.ResolvePlatform(PlatformLinuxAMD64)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.ValidateHistory(); err != nil {
		t.Fatalf("matching desktop executable rejected: %v", err)
	}
	// The retained release declares its own executable, so a desktop executable
	// that is absent from that release must be rejected even though it matches
	// the current release.
	resolved.ReleaseHistory.Releases[1].Application.Executables[0].Name = "missing"
	if err := resolved.ValidateHistory(); err == nil {
		t.Fatal("desktop executable absent from the release override accepted")
	}
}

// appImageRetainedManifest mirrors a packaging migration: the current release
// is an AppImage that uses the shared declaration, while the retained zip
// overrides the executable. History validation must resolve each release
// against its own declaration even when the retained release is selected.
const appImageRetainedManifest = `schema: 5
id: tomba
name: Tomba
summary: Demo
homepage: https://example.invalid/
categories: [games]
release:
  current: "2.0.0"
  verification:
    algorithm: sha256
  releases:
    - version: "2.0.0"
      artifacts:
        linux-amd64:
          url: https://example.invalid/app.AppImage
          verification:
            digest: "1111111111111111111111111111111111111111111111111111111111111111"
            source: https://example.invalid/app.AppImage.sha256
          archive: appimage
    - version: "1.0.0"
      executable:
        path: legacy-game
        create-bin-link: false
      artifacts:
        linux-amd64:
          url: https://example.invalid/app.zip
          verification:
            digest: "2222222222222222222222222222222222222222222222222222222222222222"
            source: https://example.invalid/app.zip.sha256
          archive: zip
application:
  executable:
    path: appimage
    name: app
    create-bin-link: false
desktop:
  working-directory: application-root
  categories: [Game]
`

func TestSelectingOverridingReleaseKeepsSharedApplicationForHistory(t *testing.T) {
	document, err := ParseBytes([]byte(appImageRetainedManifest))
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}
	resolved, err := document.ResolvePlatform(PlatformLinuxAMD64)
	if err != nil {
		t.Fatalf("ResolvePlatform() error = %v", err)
	}
	retained := resolved.SelectRelease(resolved.ReleaseHistory.Releases[1])
	if got := retained.Application.Executables[0].Path; got != "legacy-game" {
		t.Fatalf("retained executable path = %q", got)
	}
	if _, err := retained.ResolvedPackageFingerprint(); err != nil {
		t.Fatalf("retained ResolvedPackageFingerprint() error = %v", err)
	}
	current := retained.SelectRelease(resolved.ReleaseHistory.Releases[0])
	if got := current.Application.Executables[0].Path; got != "appimage" {
		t.Fatalf("reselecting the shared release kept %q", got)
	}
	if _, err := current.ResolvedPackageFingerprint(); err != nil {
		t.Fatalf("current ResolvedPackageFingerprint() error = %v", err)
	}
}

func TestSelectReleaseSwapsEffectiveExecutables(t *testing.T) {
	document, err := ParseBytes([]byte(twoReleaseManifest))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := document.ResolvePlatform(PlatformLinuxAMD64)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Application.Executables; len(got) != 1 || got[0].Name != "newtool" {
		t.Fatalf("default projection executables = %#v", got)
	}
	retained := resolved.SelectRelease(resolved.ReleaseHistory.Releases[1])
	if got := retained.Application.Executables; len(got) != 1 || got[0].Name != "oldtool" || got[0].Path != "oldtool" {
		t.Fatalf("retained projection executables = %#v", got)
	}
	if got := resolved.Application.Executables; len(got) != 1 || got[0].Name != "newtool" {
		t.Fatalf("SelectRelease mutated the receiver: %#v", got)
	}
}

func TestReleaseExecutableOverrideFingerprint(t *testing.T) {
	base := parsePackage(t, validManifest, PlatformLinuxAMD64).Fingerprint
	override := strings.Replace(validManifest, "    - version: 4.2.4\n", "    - version: 4.2.4\n      executable:\n        path: bin/renamed\n", 1)
	if got := parsePackage(t, override, PlatformLinuxAMD64).Fingerprint; got == base {
		t.Fatal("release executable override did not change the fingerprint")
	}

	// An application-level executable change must not change a retained release
	// that pins its own executable.
	before := retainedFingerprint(t, twoReleaseManifest)
	afterManifest := strings.Replace(twoReleaseManifest, "      linux-amd64: shared-amd64\n      linux-arm64: shared-arm64\n", "      linux-amd64: changed-amd64\n      linux-arm64: changed-arm64\n", 1)
	if afterManifest == twoReleaseManifest {
		t.Fatal("test fixture did not change the shared executable")
	}
	after := retainedFingerprint(t, afterManifest)
	if before != after {
		t.Fatalf("retained release fingerprint changed with the shared executable: %q != %q", before, after)
	}
}
