package manifest

import (
	"encoding/binary"
	"strings"
	"testing"
)

const validManifest = `schema: 3
id: blender
name: Blender
summary: 3D creation suite
homepage: https://www.blender.org/
categories:
  - game-development
  - graphics
platform:
  os: linux
  arch: amd64
release:
  default-channel: stable
  channels:
    stable:
      current: "5.2.0"
  releases:
    - channel: stable
      version: "5.2.0"
      url: https://download.blender.org/release/Blender5.2/blender.tar.xz
      verification:
        algorithm: sha256
        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
        source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256
      archive: tar.xz
application:
  executables:
    - name: blender
      path: blender
desktop:
  enabled: true
  categories:
    - Graphics
  icon:
    path: icons/blender.png
`

func TestParseValidManifest(t *testing.T) {
	m, err := Parse(strings.NewReader(validManifest))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if m.ID != "blender" || m.Release.Version != "5.2.0" {
		t.Fatalf("unexpected manifest: %#v", m)
	}
	if m.Desktop.Icon.Path != "icons/blender.png" {
		t.Fatalf("desktop icon = %#v", m.Desktop.Icon)
	}
	if m.Desktop.Icon.Remote() {
		t.Fatal("archive icon unexpectedly reported as remote")
	}
}

func TestExecutableIntegrationFields(t *testing.T) {
	value := strings.Replace(validManifest, "    - name: blender\n      path: blender", "    - path: bin/blender", 1)
	value = strings.Replace(value, "  categories:\n    - Graphics\n  icon:", "  executable: blender\n  working-directory: application-root\n  categories:\n    - Graphics\n  icon:", 1)
	m, err := Parse(strings.NewReader(value))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if m.Application.Executables[0].Name != "blender" || !m.Application.Executables[0].WantsBinLink() {
		t.Fatalf("derived executable = %#v", m.Application.Executables[0])
	}
	value = strings.Replace(validManifest, "      path: blender", "      path: blender\n      create-bin-link: false", 1)
	value = strings.Replace(value, "  categories:\n    - Graphics\n  icon:", "  executable: blender\n  working-directory: application-root\n  categories:\n    - Graphics\n  icon:", 1)
	m, err = Parse(strings.NewReader(value))
	if err != nil || m.Application.Executables[0].CreateBinLink == nil || *m.Application.Executables[0].CreateBinLink != false || m.Desktop.Executable != "blender" {
		t.Fatalf("explicit integration fields = %#v, %v", m, err)
	}
}

func TestRejectsAmbiguousDesktopAndWorkingDirectory(t *testing.T) {
	value := strings.Replace(validManifest, "    - name: blender\n      path: blender", "    - name: one\n      path: one\n    - name: two\n      path: two", 1)
	if _, err := Parse(strings.NewReader(value)); err == nil {
		t.Fatal("desktop with multiple executables accepted")
	}
	value = strings.Replace(validManifest, "  categories:\n    - Graphics", "  working-directory: /tmp\n  categories:\n    - Graphics", 1)
	if _, err := Parse(strings.NewReader(value)); err == nil {
		t.Fatal("arbitrary working directory accepted")
	}
}

func remoteIconBlock(url string) string {
	return "  icon:\n    url: " + url + "\n    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}

func TestParseAcceptsVerifiedRemoteIcon(t *testing.T) {
	const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, url := range []string{
		"https://pcsx2.net/app/AppIconLarge.png",
		"https://download.dinosaur.app/icons/512.png",
		"https://openrct2.org/icons/icon_x512.png",
		"https://xemu.app/xemu_512x512.png",
		"https://download.blender.org/icons/hicolor/512x512/apps/blender.png",
	} {
		t.Run(url, func(t *testing.T) {
			value := strings.Replace(validManifest, "  icon:\n    path: icons/blender.png", remoteIconBlock(url), 1)
			m, err := Parse(strings.NewReader(value))
			if err != nil {
				t.Fatalf("Parse() remote icon error = %v", err)
			}
			if !m.Desktop.Icon.Remote() || m.Desktop.Icon.SHA256 != digest {
				t.Fatalf("remote icon = %#v", m.Desktop.Icon)
			}
		})
	}
}

func TestParseRequiresExplicitDesktopIconDisposition(t *testing.T) {
	omitted := strings.Replace(validManifest, "  icon:\n    path: icons/blender.png\n", "", 1)
	if _, err := Parse(strings.NewReader(omitted)); err == nil || !strings.Contains(err.Error(), "desktop icon must be explicitly declared or null") {
		t.Fatalf("omitted desktop icon error = %v", err)
	}

	accepted := strings.Replace(validManifest, "  icon:\n    path: icons/blender.png", "  icon: null", 1)
	m, err := Parse(strings.NewReader(accepted))
	if err != nil {
		t.Fatalf("explicit null icon error = %v", err)
	}
	if !m.Desktop.Icon.IsZero() {
		t.Fatalf("explicit null icon = %#v", m.Desktop.Icon)
	}
}

func TestParseRejectsInvalidDesktopIcon(t *testing.T) {
	tests := map[string]string{
		"scalar":                  "  icon: icons/blender.png",
		"empty mapping":           "  icon: {}",
		"neither path nor url":    "  icon:\n    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"both path and url":       "  icon:\n    path: icons/blender.png\n    url: https://download.blender.org/icons/hicolor/512x512/apps/blender.png\n    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"path with remote fields": "  icon:\n    path: icons/blender.png\n    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"remote with stray path":  "  icon:\n    path: icons/blender.png\n    url: https://download.blender.org/icons/hicolor/512x512/apps/blender.png",
		"path traversal":          "  icon:\n    path: ../blender.png",
		"http url":                remoteIconBlock("http://download.blender.org/icons/hicolor/512x512/apps/blender.png"),
		"uppercase sha256":        "  icon:\n    url: https://download.blender.org/icons/hicolor/512x512/apps/blender.png\n    sha256: " + strings.Repeat("0", 63) + "A",
		"malformed sha256":        "  icon:\n    url: https://download.blender.org/icons/hicolor/512x512/apps/blender.png\n    sha256: xyz",
		"non-png extension":       remoteIconBlock("https://download.blender.org/icons/hicolor/512x512/apps/blender.svg"),
		"unknown icon field":      "  icon:\n    path: icons/blender.png\n    alt: fallback.png",
	}
	for name, block := range tests {
		t.Run(name, func(t *testing.T) {
			value := strings.Replace(validManifest, "  icon:\n    path: icons/blender.png", block, 1)
			if _, err := Parse(strings.NewReader(value)); err == nil {
				t.Fatal("invalid desktop icon unexpectedly accepted")
			}
		})
	}
}

func TestParseRejectsIconWhenDesktopDisabled(t *testing.T) {
	value := strings.Replace(strings.Replace(validManifest, "enabled: true", "enabled: false", 1), "categories:\n    - Graphics", "categories: []", 1)
	if _, err := Parse(strings.NewReader(value)); err == nil {
		t.Fatal("desktop icon without desktop integration accepted")
	}
}

func TestParseRejectsAppImageArchiveIcon(t *testing.T) {
	appImage := strings.Replace(validManifest, "archive: tar.xz", "archive: appimage", 1)
	appImage = strings.Replace(appImage, "      path: blender", "      path: appimage", 1)
	if _, err := Parse(strings.NewReader(appImage)); err == nil {
		t.Fatal("AppImage archive-contained icon unexpectedly accepted")
	}
}

func TestParseAcceptsAppImageRemoteIcon(t *testing.T) {
	appImage := strings.Replace(validManifest, "archive: tar.xz", "archive: appimage", 1)
	appImage = strings.Replace(appImage, "      path: blender", "      path: appimage", 1)
	value := strings.Replace(appImage, "  icon:\n    path: icons/blender.png", remoteIconBlock("https://pcsx2.net/app/AppIconLarge.png"), 1)
	m, err := Parse(strings.NewReader(value))
	if err != nil {
		t.Fatalf("Parse() AppImage remote icon error = %v", err)
	}
	if !m.Desktop.Icon.Remote() {
		t.Fatalf("AppImage remote icon = %#v", m.Desktop.Icon)
	}
}

func minimalPNG(size int) []byte {
	data := make([]byte, 29)
	copy(data[0:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	binary.BigEndian.PutUint32(data[8:12], 13)
	copy(data[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(data[16:20], uint32(size))
	binary.BigEndian.PutUint32(data[20:24], uint32(size))
	data[24], data[25], data[26], data[27], data[28] = 8, 2, 0, 0, 0
	return data
}

func TestIconSizeFromPNG(t *testing.T) {
	for _, want := range []int{16, 48, 256, 512} {
		got, err := IconSizeFromPNG(minimalPNG(want))
		if err != nil {
			t.Fatalf("IconSizeFromPNG(%d) = %v", want, err)
		}
		if got != want {
			t.Fatalf("IconSizeFromPNG(%d) = %d", want, got)
		}
	}
	nonSquare := minimalPNG(512)
	binary.BigEndian.PutUint32(nonSquare[20:24], 256)
	zero := minimalPNG(0)
	wrongChunk := minimalPNG(512)
	copy(wrongChunk[12:16], []byte("BLK1"))
	wrongLength := minimalPNG(512)
	binary.BigEndian.PutUint32(wrongLength[8:12], 12)
	tests := map[string][]byte{
		"short":         {0x89, 'P', 'N'},
		"bad signature": []byte("this is not a png file at all"),
		"wrong chunk":   wrongChunk,
		"wrong length":  wrongLength,
		"zero size":     zero,
		"non-square":    nonSquare,
		"unsupported":   minimalPNG(100),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := IconSizeFromPNG(data); err == nil {
				t.Fatal("invalid PNG unexpectedly accepted")
			}
		})
	}
}

func TestParseAcceptsArm64Manifest(t *testing.T) {
	arm64Manifest := strings.Replace(validManifest, "arch: amd64", "arch: arm64", 1)
	if _, err := Parse(strings.NewReader(arm64Manifest)); err != nil {
		t.Fatalf("Parse() arm64 error = %v", err)
	}
}

func TestParseAcceptsWellFormedSHA512Verification(t *testing.T) {
	sha512Manifest := strings.Replace(validManifest,
		"algorithm: sha256\n    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"algorithm: sha512\n    digest: "+strings.Repeat("0", 128), 1)
	if _, err := Parse(strings.NewReader(sha512Manifest)); err != nil {
		t.Fatalf("Parse() SHA-512 error = %v", err)
	}
}

func TestRequirements(t *testing.T) {
	valid := strings.Replace(validManifest, "platform:\n", "requirements:\n  - original-game-data\nplatform:\n", 1)
	item, err := Parse(strings.NewReader(valid))
	if err != nil || len(item.Requirements) != 1 || item.Requirements[0] != "original-game-data" {
		t.Fatalf("requirements parse = %#v, error = %v", item, err)
	}
	for name, value := range map[string]string{
		"unknown":   "  - network\n",
		"duplicate": "  - original-game-data\n  - original-game-data\n",
		"empty":     "requirements: []\n",
		"scalar":    "requirements: original-game-data\n",
		"number":    "requirements:\n  - 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			mutated := strings.Replace(validManifest, "platform:\n", "requirements:\n"+value+"platform:\n", 1)
			if _, err := Parse(strings.NewReader(mutated)); err == nil {
				t.Fatal("invalid requirements unexpectedly accepted")
			}
		})
	}
}

func TestParseApplicationCategories(t *testing.T) {
	for _, category := range []string{"game-development", "emulation", "graphics", "development", "utilities", "games", "recompilation"} {
		t.Run(category, func(t *testing.T) {
			value := strings.Replace(validManifest, "categories:\n  - game-development\n  - graphics", "categories:\n  - "+category, 1)
			if _, err := Parse(strings.NewReader(value)); err != nil {
				t.Fatalf("Parse() category %q error = %v", category, err)
			}
		})
	}
}

func TestValidateHistoryRejectsAmbiguousAndUnreviewedTargets(t *testing.T) {
	tests := map[string]string{
		"unknown default channel":             strings.Replace(validManifest, "default-channel: stable", "default-channel: nightly", 1),
		"unknown channel head":                strings.Replace(validManifest, "  channels:\n    stable:\n      current: \"5.2.0\"", "  channels:\n    stable:\n      current: \"missing\"", 1),
		"duplicate release":                   strings.Replace(validManifest, "      archive: tar.xz\napplication:", "      archive: tar.xz\n    - channel: stable\n      version: \"5.2.0\"\n      url: https://download.blender.org/release/Blender5.2/blender.tar.xz\n      verification:\n        algorithm: sha256\n        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n        source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256\n      archive: tar.xz\napplication:", 1),
		"same version across channels":        strings.Replace(validManifest, "    - channel: stable\n      version: \"5.2.0\"", "    - channel: nightly\n      version: \"5.2.0\"\n      url: https://download.blender.org/release/Blender5.2/blender.tar.xz\n      verification:\n        algorithm: sha256\n        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n        source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256\n      archive: tar.xz\n    - channel: stable\n      version: \"5.2.0\"", 1),
		"version conflicts with channel name": strings.Replace(validManifest, "version: \"5.2.0\"", "version: stable", 1),
		"malformed channel":                   strings.Replace(validManifest, "stable:\n      current", "Stable:\n      current", 1),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(value)); err == nil {
				t.Fatal("invalid history unexpectedly accepted")
			}
		})
	}
}

func TestResolveDefaultUsesExplicitChannelHead(t *testing.T) {
	manifest, err := Parse(strings.NewReader(validManifest))
	if err != nil {
		t.Fatal(err)
	}
	manifest.ReleaseHistory.Releases = append(manifest.ReleaseHistory.Releases, Release{
		Channel: "stable", Version: "1.0", URL: "https://download.blender.org/release/Blender5.2/old.tar.xz",
		Verification: Verification{Algorithm: "sha256", Digest: strings.Repeat("1", 64), Source: "https://download.blender.org/release/Blender5.2/old.tar.xz.sha256"}, Archive: "tar.xz",
	})
	got, err := manifest.ReleaseHistory.ResolveDefault()
	if err != nil || got.Version != "5.2.0" {
		t.Fatalf("ResolveDefault() = %#v, error = %v", got, err)
	}
}

func TestReleaseNestedArchiveIsReleaseScoped(t *testing.T) {
	data := strings.Replace(validManifest, "      archive: tar.xz\napplication:", "      archive: tar.xz\n      nested-archive:\n        path: packages/payload.zip\n        archive: zip\napplication:", 1)
	parsed, err := ParseBytes([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Release.NestedArchive.Path != "packages/payload.zip" || parsed.Release.NestedArchive.Archive != "zip" {
		t.Fatalf("nested recipe = %#v", parsed.Release.NestedArchive)
	}
}

func TestRejectsEmptyNestedArchiveDeclaration(t *testing.T) {
	data := strings.Replace(validManifest, "      archive: tar.xz\napplication:", "      archive: tar.xz\n      nested-archive: {}\napplication:", 1)
	if _, err := ParseBytes([]byte(data)); err == nil {
		t.Fatal("empty nested archive declaration unexpectedly accepted")
	}
}

func TestParseRejectsUnknownAndDuplicateApplicationCategories(t *testing.T) {
	tests := map[string]string{
		"unknown category":   "categories:\n  - games\n  - unknown",
		"duplicate category": "categories:\n  - games\n  - games",
	}
	for name, categories := range tests {
		t.Run(name, func(t *testing.T) {
			value := strings.Replace(validManifest, "categories:\n  - game-development\n  - graphics", categories, 1)
			if _, err := Parse(strings.NewReader(value)); err == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			}
		})
	}
}

func TestParseRejectsInvalidManifest(t *testing.T) {
	tests := map[string]func(string) string{
		"unknown field": func(s string) string { return s + "script: echo unsafe\n" },
		"script-like nested field": func(s string) string {
			return strings.Replace(s, "      path: blender", "      path: blender\n      command: ./blender", 1)
		},
		"missing verification": func(s string) string {
			return strings.Replace(s, "      verification:\n        algorithm: sha256\n        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n        source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256\n", "", 1)
		},
		"malformed digest": func(s string) string {
			return strings.Replace(s, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "xyz", 1)
		},
		"unsupported algorithm": func(s string) string {
			return strings.Replace(s, "algorithm: sha256", "algorithm: md5", 1)
		},
		"uppercase digest": func(s string) string {
			return strings.Replace(s, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef", 1)
		},
		"invalid verification source": func(s string) string {
			return strings.Replace(s, "source: https://download.blender.org", "source: http://download.blender.org", 1)
		},
		"verification source credentials": func(s string) string {
			return strings.Replace(s, "source: https://download.blender.org", "source: https://user:pass@download.blender.org", 1)
		},
		"verification source is release": func(s string) string {
			return strings.Replace(s, "source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256", "source: https://download.blender.org/release/Blender5.2/blender.tar.xz", 1)
		},
		"HTTP URL": func(s string) string {
			return strings.Replace(s, "https://download.blender.org", "http://download.blender.org", 1)
		},
		"noncanonical URL path": func(s string) string {
			return strings.Replace(s, "/release/Blender5.2/blender.tar.xz", "/release/Blender5.2/../blender.tar.xz", 1)
		},
		"invalid ID":          func(s string) string { return strings.Replace(s, "id: blender", "id: ../Blender", 1) },
		"absolute executable": func(s string) string { return strings.Replace(s, "      path: blender", "      path: /blender", 1) },
		"path traversal":      func(s string) string { return strings.Replace(s, "      path: blender", "      path: ../blender", 1) },
		"icon when disabled": func(s string) string {
			return strings.Replace(strings.Replace(s, "enabled: true", "enabled: false", 1), "categories:\n    - Graphics\n  icon:", "categories: []\n  icon:", 1)
		},
		"Windows path": func(s string) string {
			return strings.Replace(s, "      path: blender", `      path: 'C:\\blender.exe'`, 1)
		},
		"unsupported archive": func(s string) string { return strings.Replace(s, "archive: tar.xz", "archive: 7z", 1) },
		"unsupported OS":      func(s string) string { return strings.Replace(s, "os: linux", "os: windows", 1) },
		"second document":     func(s string) string { return s + "---\nschema: 3\n" },
		"YAML alias":          func(s string) string { return strings.Replace(s, "name: Blender", "name: &n Blender", 1) },
		"missing desktop enabled": func(s string) string {
			return strings.Replace(s, "  enabled: true\n", "", 1)
		},
		"missing desktop block": func(s string) string {
			return s[:strings.Index(s, "desktop:\n")]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(mutate(validManifest))); err == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			}
		})
	}
}

func TestValidateRelativePath(t *testing.T) {
	for _, good := range []string{"blender", "bin/studio", "a-b/c_d"} {
		if err := ValidateRelativePath(good); err != nil {
			t.Errorf("ValidateRelativePath(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "/x", "a/../x", "a//x", `C:\\x`, "C:x", "$HOME/x", "x%PATH%", "bin/\tapp"} {
		if err := ValidateRelativePath(bad); err == nil {
			t.Errorf("ValidateRelativePath(%q) unexpectedly succeeded", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(validManifest))
	f.Add([]byte("schema: 3\nid: ../x\n"))
	f.Fuzz(func(t *testing.T, input []byte) {
		_, _ = ParseBytes(input)
	})
}

func FuzzValidateRelativePath(f *testing.F) {
	for _, seed := range []string{"blender", "bin/studio", "../escape", `C:\\x`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		_ = ValidateRelativePath(input)
	})
}
