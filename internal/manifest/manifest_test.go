package manifest

import (
	"strings"
	"testing"
)

const validManifest = `schema: 1
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
  version: "5.2.0"
  url: https://download.blender.org/release/Blender5.2/blender.tar.xz
  verification:
    algorithm: sha256
    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256
  archive: tar.xz
application:
  executable: blender
desktop:
  enabled: true
  categories:
    - Graphics
  icon: icons/blender.png
`

func TestParseValidManifest(t *testing.T) {
	m, err := Parse(strings.NewReader(validManifest))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if m.ID != "blender" || m.Release.Version != "5.2.0" {
		t.Fatalf("unexpected manifest: %#v", m)
	}
	if m.Desktop.Icon != "icons/blender.png" {
		t.Fatalf("desktop icon = %q", m.Desktop.Icon)
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

func TestParseApplicationCategories(t *testing.T) {
	for _, category := range []string{"game-development", "emulation", "graphics", "development", "utilities", "games"} {
		t.Run(category, func(t *testing.T) {
			value := strings.Replace(validManifest, "categories:\n  - game-development\n  - graphics", "categories:\n  - "+category, 1)
			if _, err := Parse(strings.NewReader(value)); err != nil {
				t.Fatalf("Parse() category %q error = %v", category, err)
			}
		})
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
			return strings.Replace(s, "  executable: blender", "  executable: blender\n  command: ./blender", 1)
		},
		"missing verification": func(s string) string {
			return strings.Replace(s, "  verification:\n    algorithm: sha256\n    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    source: https://download.blender.org/release/Blender5.2/blender.tar.xz.sha256\n", "", 1)
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
		"absolute executable": func(s string) string { return strings.Replace(s, "executable: blender", "executable: /blender", 1) },
		"path traversal":      func(s string) string { return strings.Replace(s, "executable: blender", "executable: ../blender", 1) },
		"icon path traversal": func(s string) string { return strings.Replace(s, "icon: icons/blender.png", "icon: ../blender.png", 1) },
		"icon when disabled": func(s string) string {
			return strings.Replace(strings.Replace(s, "enabled: true", "enabled: false", 1), "categories:\n    - Graphics\n  icon:", "categories: []\n  icon:", 1)
		},
		"Windows path": func(s string) string {
			return strings.Replace(s, "executable: blender", `executable: 'C:\\blender.exe'`, 1)
		},
		"unsupported archive": func(s string) string { return strings.Replace(s, "archive: tar.xz", "archive: 7z", 1) },
		"unsupported OS":      func(s string) string { return strings.Replace(s, "os: linux", "os: windows", 1) },
		"second document":     func(s string) string { return s + "---\nschema: 1\n" },
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
	f.Add([]byte("schema: 1\nid: ../x\n"))
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
