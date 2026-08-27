package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
)

const iconTestManifest = `schema: 4
id: demo
name: Demo
summary: Demo
homepage: https://example.com/
categories: [graphics]
platforms:
  linux-amd64:
    release:
      default-channel: stable
      channels:
        stable:
          current: "1.0.0"
      releases:
        - channel: stable
          version: "1.0.0"
          url: https://github.com/example/demo/releases/download/v1.0.0/demo.tar.gz
          verification:
            algorithm: sha256
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/demo.sha256
          archive: tar.gz
    application:
      executables:
        - name: demo
          path: demo
    desktop:
      enabled: true
      categories: [Graphics]
      icon: null
`

func TestAddRemoteIconIsMinimalAndValid(t *testing.T) {
	icon := fixedRegistryIcon{URL: "https://raw.githubusercontent.com/example/demo/0123456789abcdef0123456789abcdef01234567/icon.png", SHA256: strings.Repeat("a", 64)}
	updated, err := addRemoteIcon([]byte(iconTestManifest), icon)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("      categories: [Graphics]\n      icon:\n")) {
		t.Fatalf("icon was not inserted into desktop block:\n%s", updated)
	}
	if !bytes.Contains(updated, []byte("homepage: https://example.com/\n")) {
		t.Fatal("unrelated content changed")
	}
	if _, err := addRemoteIcon(updated, icon); err == nil {
		t.Fatal("repeated repair did not skip existing icon")
	}
}

func TestAddRemoteIconUpdatesOnlyEnabledPlatformDefinitions(t *testing.T) {
	arm := strings.Replace(iconTestManifest, "linux-amd64:", "linux-arm64:", 1)
	start := strings.Index(arm, "  linux-arm64:")
	dual := iconTestManifest + strings.Replace(arm[start:], "enabled: true", "enabled: false", 1)
	icon := fixedRegistryIcon{URL: "https://raw.githubusercontent.com/example/demo/0123456789abcdef0123456789abcdef01234567/icon.png", SHA256: strings.Repeat("a", 64)}
	updated, err := addRemoteIcon([]byte(dual), icon)
	if err != nil {
		t.Fatal(err)
	}
	if count := bytes.Count(updated, []byte("      icon:\n")); count != 1 {
		t.Fatalf("inserted %d icons, want one enabled-platform icon\n%s", count, updated)
	}
	if count := bytes.Count(updated, []byte("      icon: null\n")); count != 1 {
		t.Fatalf("disabled platform null icon was changed\n%s", updated)
	}
	if _, err := manifest.ParseBytes(updated); err != nil {
		t.Fatalf("updated dual-platform manifest is invalid: %v", err)
	}
}

func TestIconPathScorePrefersKnownApplicationPaths(t *testing.T) {
	if iconPathScore("misc/logo/icon.png") <= iconPathScore("logo.png") {
		t.Fatal("Godot-style path was not preferred")
	}
	if iconPathScore("assets/graphics/icons/icon.png") != 98 {
		t.Fatal("Pixelorama-style score changed")
	}
}

func TestFallbackIconRankingPrefers512InIcons(t *testing.T) {
	if fallbackTreeScore("icons/512.png") <= fallbackTreeScore("logo.png") {
		t.Fatal("explicit icons path did not outrank generic logo")
	}
	if fallbackIconScore("icons/512.png", 512) <= fallbackIconScore("icons/256.png", 256) {
		t.Fatal("512 icon did not outrank 256 icon")
	}
}

func TestFallbackIconCandidatesAreNarrow(t *testing.T) {
	if fallbackTreeScore("screenshots/512.png") != 0 {
		t.Fatal("unrelated PNG was treated as an icon")
	}
	if fallbackTreeScore("icons/512.svg") != 0 {
		t.Fatal("non-PNG was treated as an icon")
	}
}
