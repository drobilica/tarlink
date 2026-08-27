package registrycheck

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
)

const checkerManifest = `schema: 4
id: fixture
name: Fixture
summary: Fixture application
homepage: https://example.com/
categories: [utilities]
platforms:
  linux-amd64:
    release:
      default-channel: stable
      channels:
        stable:
          current: "1.0"
      releases:
        - channel: stable
          version: "1.0"
          url: https://example.com/fixture.tar.gz
          verification:
            algorithm: sha256
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.com/SHA256SUMS
          archive: tar.gz
    application: {executables: [{name: fixture, path: fixture}]}
    desktop: {enabled: false, categories: []}
`

const checkerLegacyV3 = `schema: 3
id: fixture
name: Fixture
summary: Fixture application
homepage: https://example.com/
categories: [utilities]
platform: {os: linux, arch: amd64}
release:
  default-channel: stable
  channels:
    stable: {current: "1.0"}
  releases:
    - channel: stable
      version: "1.0"
      url: https://example.com/fixture.tar.gz
      verification:
        algorithm: sha256
        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
        source: https://example.com/SHA256SUMS
      archive: tar.gz
application: {executables: [{name: fixture, path: fixture}]}
desktop: {enabled: false, categories: []}
`

func writeLegacyV3Registry(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "apps", "fixture")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "linux-amd64.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeCheckerRegistry(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "apps", "fixture")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "manifest.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeCheckerManifest(t *testing.T, root, id, arch string) {
	t.Helper()
	directory := filepath.Join(root, "apps", id)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(checkerManifest, "id: fixture", "id: "+id)
	body = strings.ReplaceAll(body, "name: Fixture", "name: "+strings.Title(id))
	path := filepath.Join(directory, "manifest.yaml")
	if arch == "arm64" {
		arm := strings.Replace(body, "linux-amd64:", "linux-arm64:", 1)
		start := strings.Index(arm, "  linux-arm64:")
		body = strings.Replace(body, "platforms:\n", "platforms:\n"+arm[start:], 1)
	}
	if existing, err := os.ReadFile(path); err == nil && arch == "amd64" {
		body = string(existing)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func checkerDualPlatform() string {
	arm := strings.Replace(checkerManifest, "linux-amd64:", "linux-arm64:", 1)
	start := strings.Index(arm, "  linux-arm64:")
	return checkerManifest + arm[start:]
}

func mutateCheckerPlatform(body, key string, mutate func(string) string) string {
	start := strings.Index(body, "  "+key+":")
	if start < 0 {
		return body
	}
	end := len(body)
	if next := strings.Index(body[start+2:], "\n  linux-"); next >= 0 {
		end = start + 2 + next + 1
	}
	return body[:start] + mutate(body[start:end]) + body[end:]
}

func TestAppSelectsAllArchitecturesDeterministically(t *testing.T) {
	root := t.TempDir()
	writeCheckerManifest(t, root, "k9s", "arm64")
	writeCheckerManifest(t, root, "k9s", "amd64")
	writeCheckerManifest(t, root, "other", "amd64")

	selection, err := App(root, "k9s")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 2 {
		t.Fatalf("selected %d items, want 2", len(selection.Items))
	}
	if got := selection.Items[0].Platform.Arch; got != "amd64" {
		t.Fatalf("first architecture = %q, want amd64", got)
	}
	if got := selection.Items[1].Platform.Arch; got != "arm64" {
		t.Fatalf("second architecture = %q, want arm64", got)
	}
	for _, item := range selection.Items {
		if item.ID != "k9s" {
			t.Fatalf("selected unrelated application %q", item.ID)
		}
	}
}

func TestAppSelectsSingleArchitectureAndRejectsUnknown(t *testing.T) {
	root := writeCheckerRegistry(t, checkerManifest)
	selection, err := App(root, "fixture")
	if err != nil || len(selection.Items) != 1 {
		t.Fatalf("single architecture selection = %#v, error = %v", selection.Items, err)
	}
	if _, err := App(root, "missing"); err == nil {
		t.Fatal("unknown application unexpectedly selected")
	}
}

func TestAppProjectsEveryApprovedHistoricalRelease(t *testing.T) {
	body := strings.Replace(checkerManifest, `    application:`, `        - channel: stable
          version: "0.9"
          url: https://example.com/fixture-0.9.tar.gz
          verification:
            algorithm: sha256
            digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789
            source: https://example.com/SHA256SUMS-0.9
          archive: tar.gz
    application:`, 1)
	root := writeCheckerRegistry(t, body)
	selection, err := App(root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 2 {
		t.Fatalf("selected %d historical releases, want 2", len(selection.Items))
	}
	if selection.Items[0].Release.Version != "0.9" || selection.Items[1].Release.Version != "1.0" {
		t.Fatalf("release projections = %q, %q", selection.Items[0].Release.Version, selection.Items[1].Release.Version)
	}
}

func checkerArchive(t *testing.T, executable, body string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	prefix := "fixture/"
	entries := []tar.Header{
		{Name: prefix, Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + "bin/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: prefix + executable, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))},
	}
	for _, header := range entries {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func checkerMaterializeManifest(server *httptest.Server, data []byte, executableName, executablePath string) *manifest.Manifest {
	digest := sha256.Sum256(data)
	release := manifest.Release{Channel: "stable", Version: "1.0", URL: server.URL, Verification: manifest.Verification{
		Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.URL + "/SHA256SUMS",
	}, Archive: "tar.gz"}
	return &manifest.Manifest{
		Schema: 4, ID: "fixture", Name: "Fixture", Summary: "Lifecycle fixture", Homepage: "https://example.com/",
		Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: release, ReleaseHistory: manifest.ReleaseHistory{DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: "1.0"}}, Releases: []manifest.Release{release}},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: executableName, Path: executablePath}}},
		Desktop:     manifest.Desktop{Enabled: false, Categories: []string{}},
	}
}

func TestMaterializeWithClientLifecycleAndNoExecution(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	data := checkerArchive(t, "bin/run", "#!/bin/sh\necho executed > "+marker+"\n")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(data)
	}))
	defer server.Close()
	client := &download.Client{HTTP: server.Client(), RedirectLimit: 2}
	if err := MaterializeWithClient(context.Background(), checkerMaterializeManifest(server, data, "run", "bin/run"), client); err != nil {
		t.Fatalf("MaterializeWithClient() error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("synthetic executable ran: %v", err)
	}
}

func TestMaterializeWithClientReportsMaterializationFailures(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		mutate     func(*manifest.Manifest)
	}{
		{name: "checksum mismatch", executable: "bin/run", mutate: func(item *manifest.Manifest) {
			item.Release.Verification.Digest = strings.Repeat("0", 64)
		}},
		{name: "missing executable", executable: "bin/missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := checkerArchive(t, "bin/run", "not executed")
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write(data)
			}))
			defer server.Close()
			item := checkerMaterializeManifest(server, data, "run", test.executable)
			if test.mutate != nil {
				test.mutate(item)
			}
			client := &download.Client{HTTP: server.Client(), RedirectLimit: 2}
			if err := MaterializeWithClient(context.Background(), item, client); err == nil {
				t.Fatal("MaterializeWithClient() unexpectedly succeeded")
			}
		})
	}
}

func TestChangedClassifiesMaterializationAndCatalogChanges(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	newRoot := writeCheckerRegistry(t, strings.Replace(checkerManifest, "Fixture application", "A better summary", 1))
	selection, err := Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 0 {
		t.Fatalf("metadata-only change selected %#v", selection.Items)
	}

	// A release change is represented by appending a new immutable approved
	// release and advancing the channel head; the existing 1.0 entry remains
	// in history.
	changed := strings.Replace(checkerManifest, "      current: \"1.0\"", "      current: \"2.0\"", 1)
	changed = strings.Replace(changed, "    application:", `        - channel: stable
          version: "2.0"
          url: https://example.com/fixture-2.0.tar.gz
          verification:
            algorithm: sha256
            digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
            source: https://example.com/SHA256SUMS-2.0
          archive: tar.gz
    application:`, 1)
	newRoot = writeCheckerRegistry(t, changed)
	selection, err = Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].ID != "fixture" || selection.Items[0].Release.Version != "2.0" {
		t.Fatalf("artifact change selection = %#v", selection.Items)
	}
}

func TestChangedComparesUnifiedManifestPerPlatform(t *testing.T) {
	dual := checkerDualPlatform()
	tests := []struct {
		name      string
		oldBody   string
		newBody   func(string) string
		wantArch  string
		wantCount int
		wantError bool
	}{
		{
			name:    "amd64 release change only",
			oldBody: dual,
			newBody: func(body string) string {
				return mutateCheckerPlatform(body, "linux-amd64", func(platform string) string {
					platform = strings.Replace(platform, `current: "1.0"`, `current: "2.0"`, 1)
					return strings.Replace(platform, "    application:", `        - channel: stable
          version: "2.0"
          url: https://example.com/fixture-2.0.tar.gz
          verification: {algorithm: sha256, digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef, source: https://example.com/SHA256SUMS-2.0}
          archive: tar.gz
    application:`, 1)
				})
			},
			wantArch: "amd64", wantCount: 1,
		},
		{
			name:    "arm64 digest change only",
			oldBody: dual,
			newBody: func(body string) string {
				return mutateCheckerPlatform(body, "linux-arm64", func(platform string) string {
					platform = strings.Replace(platform, "linux-arm64:\n", "linux-arm64:\n    revision: 2\n", 1)
					return strings.Replace(platform, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", 1)
				})
			},
			wantArch: "arm64", wantCount: 1,
		},
		{
			name:    "shared material metadata changes both",
			oldBody: dual,
			newBody: func(body string) string {
				body = strings.Replace(body, "name: Fixture", "name: Renamed Fixture", 1)
				body = strings.ReplaceAll(body, "    release:\n", "    revision: 2\n    release:\n")
				return body
			},
			wantCount: 2,
		},
		{
			name: "formatting only", oldBody: dual,
			newBody:   func(body string) string { return strings.Replace(body, "name: Fixture\n", "name: Fixture\n\n", 1) },
			wantCount: 0,
		},
		{
			name: "new arm64 entry", oldBody: checkerManifest,
			newBody:  func(string) string { return dual },
			wantArch: "arm64", wantCount: 1,
		},
		{
			name: "removed arm64 entry", oldBody: dual,
			newBody:   func(string) string { return checkerManifest },
			wantError: true,
		},
		{
			name:    "amd64 application path only",
			oldBody: dual,
			newBody: func(body string) string {
				return mutateCheckerPlatform(body, "linux-amd64", func(platform string) string {
					platform = strings.Replace(platform, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
					return strings.Replace(platform, "path: fixture", "path: bin/fixture", 1)
				})
			},
			wantArch: "amd64", wantCount: 1,
		},
		{
			name:    "amd64 revision only",
			oldBody: dual,
			newBody: func(body string) string {
				return mutateCheckerPlatform(body, "linux-amd64", func(platform string) string {
					return strings.Replace(platform, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
				})
			},
			wantArch: "amd64", wantCount: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldRoot := writeCheckerRegistry(t, test.oldBody)
			newRoot := writeCheckerRegistry(t, test.newBody(test.oldBody))
			selection, err := Changed(newRoot, oldRoot)
			if test.wantError {
				if err == nil {
					t.Fatal("platform removal unexpectedly accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(selection.Items) != test.wantCount {
				t.Fatalf("selected %d items, want %d: %#v", len(selection.Items), test.wantCount, selection.Items)
			}
			if test.wantArch != "" && selection.Items[0].Platform.Arch != test.wantArch {
				t.Fatalf("selected architecture %q, want %q", selection.Items[0].Platform.Arch, test.wantArch)
			}
		})
	}
}

func TestChangedNewAndDeletedManifests(t *testing.T) {
	oldRoot := t.TempDir()
	newRoot := writeCheckerRegistry(t, checkerManifest)
	selection, err := Changed(newRoot, oldRoot)
	if err != nil || len(selection.Items) != 1 {
		t.Fatalf("new manifest selection = %#v, error = %v", selection.Items, err)
	}

	oldRoot = writeCheckerRegistry(t, checkerManifest)
	emptyRoot := t.TempDir()
	if err := Structural(oldRoot); err != nil {
		t.Fatal(err)
	}
	selection, err = Changed(emptyRoot, oldRoot)
	if err == nil {
		t.Fatal("empty current registry unexpectedly accepted")
	}
}

func TestChangedRejectsRemovedV2Manifest(t *testing.T) {
	oldRoot := t.TempDir()
	writeCheckerManifest(t, oldRoot, "fixture", "amd64")
	writeCheckerManifest(t, oldRoot, "fixture", "arm64")
	currentRoot := writeCheckerRegistry(t, checkerManifest)

	if _, err := Changed(currentRoot, oldRoot); err == nil {
		t.Fatal("removed v3 platform manifest unexpectedly accepted")
	}
}

func TestChangedV3ToV4MigrationDoesNotMaterializeEquivalentArtifact(t *testing.T) {
	current := writeCheckerRegistry(t, checkerManifest)
	previous := writeLegacyV3Registry(t, checkerLegacyV3)
	selection, err := Changed(current, previous)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 0 {
		t.Fatalf("schema-only migration selected unchanged artifact: %#v", selection.Items)
	}
}

func TestChangedSelectsHistoryArtifactChanges(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	// A new approved release is appended to history. The existing 1.0
	// release remains immutable and retained while the channel head advances.
	changed := strings.Replace(checkerManifest, "      current: \"1.0\"", "      current: \"2.0\"", 1)
	changed = strings.Replace(changed, "    application:", `        - channel: stable
          version: "2.0"
          url: https://example.com/fixture-2.0.tar.gz
          verification:
            algorithm: sha256
            digest: 1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef
            source: https://example.com/SHA256SUMS-2.0
          archive: tar.gz
    application:`, 1)
	newRoot := writeCheckerRegistry(t, changed)
	selection, err := Changed(newRoot, oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].Release.Version != "2.0" {
		t.Fatalf("history artifact change selection = %#v", selection.Items)
	}
}

func TestChangedRejectsHistoricalReleaseMutationOrRemoval(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	nestedMutation := strings.Replace(checkerManifest, "          archive: tar.gz\n", "          archive: tar.gz\n          nested-archive: {path: payload.zip, archive: zip}\n", 1)
	if _, err := Changed(writeCheckerRegistry(t, nestedMutation), oldRoot); err == nil {
		t.Fatal("mutated approved nested recipe unexpectedly accepted")
	}
	mutated := strings.Replace(checkerManifest, "https://example.com/fixture.tar.gz", "https://example.com/other.tar.gz", 1)
	if _, err := Changed(writeCheckerRegistry(t, mutated), oldRoot); err == nil {
		t.Fatal("mutated approved release unexpectedly accepted")
	}
	removed := strings.Replace(checkerManifest, "        - channel: stable\n          version: \"1.0\"\n          url: https://example.com/fixture.tar.gz\n          verification:\n            algorithm: sha256\n            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n            source: https://example.com/SHA256SUMS\n          archive: tar.gz\n", "", 1)
	if _, err := Changed(writeCheckerRegistry(t, removed), oldRoot); err == nil {
		t.Fatal("removed approved release unexpectedly accepted")
	}
}

func TestChangedAllowsSameVersionReleaseMutationOnlyWithRevisionBump(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	withoutBump := strings.Replace(checkerManifest, "https://example.com/fixture.tar.gz", "https://example.com/corrected.tar.gz", 1)
	if _, err := Changed(writeCheckerRegistry(t, withoutBump), oldRoot); err == nil {
		t.Fatal("same-version URL change without revision bump unexpectedly accepted")
	}
	withBump := strings.Replace(withoutBump, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
	if selection, err := Changed(writeCheckerRegistry(t, withBump), oldRoot); err != nil || len(selection.Items) != 1 {
		t.Fatalf("same-version URL change with revision bump: selection=%#v error=%v", selection.Items, err)
	}

	digestChange := strings.Replace(checkerManifest, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789", 1)
	if _, err := Changed(writeCheckerRegistry(t, digestChange), oldRoot); err == nil {
		t.Fatal("same-version digest change without revision bump unexpectedly accepted")
	}
	digestBump := strings.Replace(digestChange, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
	if _, err := Changed(writeCheckerRegistry(t, digestBump), oldRoot); err != nil {
		t.Fatalf("same-version digest change with revision bump rejected: %v", err)
	}
}

func TestRevisionBumpDoesNotBypassManifestValidation(t *testing.T) {
	invalid := strings.Replace(checkerManifest, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
	invalid = strings.Replace(invalid, "source: https://example.com/SHA256SUMS", "source: http://example.com/release", 1)
	if _, err := manifest.ParseBytes([]byte(invalid)); err == nil {
		t.Fatal("revision bump bypassed release verification-source validation")
	}
}

func TestChangedRejectsRevisionDecreaseAndAcceptsUnchangedBump(t *testing.T) {
	old := strings.Replace(checkerManifest, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
	oldRoot := writeCheckerRegistry(t, old)
	decreased := strings.Replace(old, "revision: 2", "revision: 1", 1)
	if _, err := Changed(writeCheckerRegistry(t, decreased), oldRoot); err == nil {
		t.Fatal("revision decrease unexpectedly accepted")
	}
	bumped := strings.Replace(old, "revision: 2", "revision: 3", 1)
	if _, err := Changed(writeCheckerRegistry(t, bumped), oldRoot); err != nil {
		t.Fatalf("unchanged release revision bump rejected: %v", err)
	}
}

func TestChangedAcceptsExplicitAppImageReleaseCorrection(t *testing.T) {
	oldRoot := writeCheckerRegistry(t, checkerManifest)
	corrected := strings.Replace(checkerManifest, `current: "1.0"`, `current: "1.0-appimage"`, 1)
	corrected = strings.Replace(corrected, "linux-amd64:\n", "linux-amd64:\n    revision: 2\n", 1)
	corrected = strings.Replace(corrected, `version: "1.0"`, `version: "1.0-appimage"`, 1)
	corrected = strings.Replace(corrected, "https://example.com/fixture.tar.gz", "https://example.com/fixture.AppImage", 1)
	corrected = strings.Replace(corrected, "archive: tar.gz", "archive: appimage", 1)
	corrected = strings.Replace(corrected, "path: fixture", "path: appimage", 1)
	selection, err := Changed(writeCheckerRegistry(t, corrected), oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Items) != 1 || selection.Items[0].Release.Version != "1.0-appimage" {
		t.Fatalf("correction selection = %#v", selection.Items)
	}
}
