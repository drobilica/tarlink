package app

import (
	"bytes"
	"strings"
	"testing"
)

const iconTestManifest = `schema: 3
id: demo
name: Demo
summary: Demo
homepage: https://example.com/
categories: [graphics]
platform:
  os: linux
  arch: amd64
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
`

func TestAddRemoteIconIsMinimalAndValid(t *testing.T) {
	icon := fixedRegistryIcon{URL: "https://raw.githubusercontent.com/example/demo/0123456789abcdef0123456789abcdef01234567/icon.png", SHA256: strings.Repeat("a", 64)}
	updated, err := addRemoteIcon([]byte(iconTestManifest), icon)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("  categories: [Graphics]\n  icon:\n")) {
		t.Fatalf("icon was not inserted into desktop block:\n%s", updated)
	}
	if !bytes.Contains(updated, []byte("homepage: https://example.com/\n")) {
		t.Fatal("unrelated content changed")
	}
	if _, err := addRemoteIcon(updated, icon); err == nil {
		t.Fatal("repeated repair did not skip existing icon")
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
