package manifest

import (
	"strings"
	"testing"
)

const validRuntimeManifest = `schema: 1
id: steam-linux-runtime-4
kind: steam-linux-runtime
version: 4.0.20260805.254769
platform: linux-amd64
artifact:
  url: https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SteamLinuxRuntime_4.tar.xz
  archive: tar.xz
  verification:
    algorithm: sha256
    digest: 3226d8234e7c0542ee767837832bfb1dad5e5e2dc944ec97eb221b437f6b9349
    source: https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SHA256SUMS
interface: valve-v2-entry-point-v1
`

func TestParseRuntime(t *testing.T) {
	runtime, err := ParseRuntime(strings.NewReader(validRuntimeManifest))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ID != "steam-linux-runtime-4" || runtime.Platform != (Platform{OS: "linux", Arch: "amd64"}) {
		t.Fatalf("unexpected runtime %#v", runtime)
	}
	fingerprint, err := runtime.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") {
		t.Fatalf("invalid fingerprint %q", fingerprint)
	}
}

func TestParseRuntimeRejectsUnknownKind(t *testing.T) {
	data := strings.Replace(validRuntimeManifest, "kind: steam-linux-runtime", "kind: other", 1)
	if _, err := ParseRuntime(strings.NewReader(data)); err == nil {
		t.Fatal("ParseRuntime accepted an unknown runtime kind")
	}
}

func TestRuntimeReferenceRequiresExactVersion(t *testing.T) {
	if err := (RuntimeReference{ID: "steam-linux-runtime-4", Version: ""}).Validate(); err == nil {
		t.Fatal("empty runtime version was accepted")
	}
}
