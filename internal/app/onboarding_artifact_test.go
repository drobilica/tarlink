package app

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/research"
	"github.com/ulikunitz/xz"
)

func httpsArtifactFixture(t *testing.T, responder maintainerRoundTrip) (*Maintainer, *int) {
	t.Helper()
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	transport := maintainerRoundTrip(func(request *http.Request) (*http.Response, error) {
		requests++
		return responder(request)
	})
	return &Maintainer{layout: layout, client: &download.Client{HTTP: &http.Client{Transport: transport}}}, &requests
}

func httpsArtifactOK(payload []byte) maintainerRoundTrip {
	return func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(payload)),
			Header:        make(http.Header),
			Request:       request,
			ContentLength: int64(len(payload)),
		}, nil
	}
}

func httpsArtifactTarXZ(t *testing.T, executable, icon []byte) []byte {
	t.Helper()
	var tarball bytes.Buffer
	writer := tar.NewWriter(&tarball)
	for _, entry := range []struct {
		name string
		mode int64
		body []byte
	}{
		{name: "blender-5.2.2-linux-x64/blender", mode: 0o755, body: executable},
		{name: "blender-5.2.2-linux-x64/icons/blender-icon.png", mode: 0o644, body: icon},
	} {
		if err := writer.WriteHeader(&tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: tar.TypeReg, Size: int64(len(entry.body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	compressed, err := xz.NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compressed.Write(tarball.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func httpsArtifactZip(t *testing.T, executable, icon []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range []struct {
		name string
		mode os.FileMode
		body []byte
	}{
		{name: "blender-5.2.2-linux-x64/blender", mode: 0o755, body: executable},
		{name: "blender-5.2.2-linux-x64/icons/blender-icon.png", mode: 0o644, body: icon},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func httpsArtifactPNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{0}, 32)...)
}

func checkArtifactDigests(t *testing.T, result RegistryInspectionResult, payload []byte) {
	t.Helper()
	if result.Artifact == nil {
		t.Fatalf("missing artifact inspection: %#v", result)
	}
	sha256Sum := sha256.Sum256(payload)
	sha512Sum := sha512.Sum512(payload)
	if got := result.Artifact.ComputedDigests["sha256"]; got != hex.EncodeToString(sha256Sum[:]) {
		t.Fatalf("sha256 = %q", got)
	}
	if got := result.Artifact.ComputedDigests["sha512"]; got != hex.EncodeToString(sha512Sum[:]) {
		t.Fatalf("sha512 = %q", got)
	}
}

func TestInspectHTTPSArtifactTarXZSuccess(t *testing.T) {
	payload := httpsArtifactTarXZ(t, appImageELF(0x3e), httpsArtifactPNG())
	target := "https://download.example.test/release/Blender5.2/blender-5.2.2-linux-x64.tar.xz"
	maintainer, requests := httpsArtifactFixture(t, httpsArtifactOK(payload))
	result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
	if result.Status != "ready" {
		t.Fatalf("status = %q, required = %+v", result.Status, result.Required)
	}
	if result.ArtifactURL != target || result.ArtifactSize != int64(len(payload)) {
		t.Fatalf("url/size = %q/%d", result.ArtifactURL, result.ArtifactSize)
	}
	if result.Artifact.ArtifactType != "tar.xz" {
		t.Fatalf("artifact type = %q", result.Artifact.ArtifactType)
	}
	checkArtifactDigests(t, result, payload)
	if len(result.Artifact.Executables) != 1 || result.Artifact.Executables[0] != "blender" {
		t.Fatalf("executables = %#v", result.Artifact.Executables)
	}
	if len(result.Artifact.Icons) != 1 || result.Artifact.Icons[0] != "icons/blender-icon.png" {
		t.Fatalf("icons = %#v", result.Artifact.Icons)
	}
	if len(result.Artifact.Blockers) != 0 {
		t.Fatalf("blockers = %#v", result.Artifact.Blockers)
	}
}

func TestInspectHTTPSArtifactZipSuccess(t *testing.T) {
	payload := httpsArtifactZip(t, appImageELF(0xb7), httpsArtifactPNG())
	target := "https://download.example.test/release/fixture-linux-arm64.zip"
	maintainer, requests := httpsArtifactFixture(t, httpsArtifactOK(payload))
	result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
	if result.Status != "ready" {
		t.Fatalf("status = %q, required = %+v", result.Status, result.Required)
	}
	if result.Artifact.ArtifactType != "zip" {
		t.Fatalf("artifact type = %q", result.Artifact.ArtifactType)
	}
	checkArtifactDigests(t, result, payload)
	if len(result.Artifact.Executables) != 1 || result.Artifact.Executables[0] != "blender" {
		t.Fatalf("executables = %#v", result.Artifact.Executables)
	}
	if len(result.Artifact.Icons) != 1 || result.Artifact.Icons[0] != "icons/blender-icon.png" {
		t.Fatalf("icons = %#v", result.Artifact.Icons)
	}
	if len(result.Artifact.Blockers) != 0 {
		t.Fatalf("blockers = %#v", result.Artifact.Blockers)
	}
}

func TestInspectHTTPSArtifactAppImageSuccess(t *testing.T) {
	payload := appImageELF(0x3e)
	target := "https://download.example.test/release/app-linux-amd64.AppImage"
	maintainer, requests := httpsArtifactFixture(t, httpsArtifactOK(payload))
	result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
	if err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
	if result.Status != "ready" || result.Artifact == nil {
		t.Fatalf("result = %#v", result)
	}
	if result.Artifact.ArtifactType != "appimage" {
		t.Fatalf("artifact type = %q", result.Artifact.ArtifactType)
	}
	checkArtifactDigests(t, result, payload)
}

func TestParseRegistryHTTPSArtifactURL(t *testing.T) {
	valid := []string{
		"https://download.blender.org/release/Blender5.2/blender-5.2.2-linux-x64.tar.xz",
		"https://example.test/a/b.zip",
		"https://example.test/app-linux-amd64.AppImage",
	}
	for _, target := range valid {
		if _, err := ParseRegistryHTTPSArtifactURL(target); err != nil {
			t.Fatalf("ParseRegistryHTTPSArtifactURL(%q) error = %v", target, err)
		}
	}
	invalid := []string{
		"",
		"http://example.test/a.tar.xz",
		"ftp://example.test/a.tar.xz",
		"https://example.test/a.tar.xz?x=1",
		"https://example.test/a.tar.xz#frag",
		"https://user@example.test/a.tar.xz",
		"https://user:pass@example.test/a.tar.xz",
		"//example.test/a.tar.xz",
		"relative/path.tar.xz",
		"a.tar.xz",
		"https://example.test/",
		"https://example.test",
		"https://example.test:8443/a/b.zip",
		"https://github.com/owner/repo/releases/download/v1.2.3/app-linux-amd64.tar.xz",
		"https://github.com:443/owner/repo/releases/download/v1.2.3/app-linux-amd64.tar.xz",
	}
	for _, target := range invalid {
		if _, err := ParseRegistryHTTPSArtifactURL(target); err == nil {
			t.Fatalf("ParseRegistryHTTPSArtifactURL(%q) unexpectedly accepted", target)
		} else if CodeOf(err) != CodeInvalidArguments {
			t.Fatalf("ParseRegistryHTTPSArtifactURL(%q) code = %q, want invalid_arguments", target, CodeOf(err))
		}
	}
	// GitHub release-asset URLs keep their existing routing.
	github := "https://github.com/owner/repo/releases/download/v1.2.3/app-linux-amd64.tar.xz"
	if _, err := ParseRegistryHTTPSArtifactURL(github); err == nil {
		t.Fatal("GitHub release-asset URL must not parse as a generic HTTPS artifact")
	}
	if _, err := research.ParseReleaseAssetURL(github); err != nil {
		t.Fatalf("GitHub release-asset URL no longer parses: %v", err)
	}
}

func TestInspectHTTPSArtifactRejectsHostileTargetsBeforeFetch(t *testing.T) {
	targets := []string{
		"http://example.test/a.tar.xz",
		"ftp://example.test/a.tar.xz",
		"https://example.test/a.tar.xz?x=1",
		"https://example.test/a.tar.xz#frag",
		"https://user@example.test/a.tar.xz",
		"https://github.com:443/owner/repo/releases/download/v1.2.3/app-linux-amd64.tar.xz",
	}
	for _, target := range targets {
		maintainer, requests := httpsArtifactFixture(t, httpsArtifactOK([]byte("must not fetch")))
		_, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
		if err == nil {
			t.Fatalf("InspectRegistry(%q) unexpectedly succeeded", target)
		}
		if CodeOf(err) != CodeInvalidArguments {
			t.Fatalf("InspectRegistry(%q) code = %q, want invalid_arguments", target, CodeOf(err))
		}
		if *requests != 0 {
			t.Fatalf("InspectRegistry(%q) performed %d HTTP requests, want 0", target, *requests)
		}
	}
	// A relative path that is not an existing local file keeps its existing
	// local-filesystem error, but it must still fail before any network
	// access.
	maintainer, requests := httpsArtifactFixture(t, httpsArtifactOK([]byte("must not fetch")))
	if _, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: "relative/path-that-does-not-exist.tar.xz"}); err == nil {
		t.Fatal("InspectRegistry(relative missing path) unexpectedly succeeded")
	}
	if *requests != 0 {
		t.Fatalf("relative missing path performed %d HTTP requests, want 0", *requests)
	}
}

func TestInspectHTTPSArtifactFailurePaths(t *testing.T) {
	const target = "https://download.example.test/release/fixture-linux-amd64.tar.xz"

	t.Run("oversized response rejected", func(t *testing.T) {
		maintainer, requests := httpsArtifactFixture(t, func(request *http.Request) (*http.Response, error) {
			response := httpsArtifactOK([]byte("x"))
			got, _ := response(request)
			got.ContentLength = download.DefaultMaxArtifactBytes + 1
			return got, nil
		})
		_, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
		if err == nil {
			t.Fatal("oversized response unexpectedly accepted")
		}
		if !errors.Is(err, download.ErrTooLarge) {
			t.Fatalf("error = %v, want download size-limit failure", err)
		}
		if CodeOf(err) != CodeNetwork {
			t.Fatalf("code = %q, want network_failure", CodeOf(err))
		}
		if *requests != 1 {
			t.Fatalf("requests = %d, want 1", *requests)
		}
	})

	t.Run("server error is classified", func(t *testing.T) {
		maintainer, _ := httpsArtifactFixture(t, func(request *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader("boom")), Header: make(http.Header), Request: request}, nil
		})
		result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
		if err == nil {
			t.Fatalf("server error unexpectedly succeeded: %#v", result)
		}
		if result.Artifact != nil {
			t.Fatalf("server error fabricated a result: %#v", result.Artifact)
		}
		if CodeOf(err) != CodeNetwork {
			t.Fatalf("code = %q, want network_failure", CodeOf(err))
		}
	})

	t.Run("redirect downgrade rejected", func(t *testing.T) {
		maintainer, requests := httpsArtifactFixture(t, func(request *http.Request) (*http.Response, error) {
			if strings.HasPrefix(request.URL.String(), "http://") {
				t.Fatal("http downgrade destination was requested")
			}
			header := make(http.Header)
			header.Set("Location", "http://download.example.test/release/evil.tar.xz")
			return &http.Response{StatusCode: http.StatusFound, Body: io.NopCloser(strings.NewReader("")), Header: header, Request: request}, nil
		})
		_, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
		if err == nil {
			t.Fatal("redirect downgrade unexpectedly accepted")
		}
		if CodeOf(err) != CodeNetwork {
			t.Fatalf("code = %q, want network_failure", CodeOf(err))
		}
		if *requests != 1 {
			t.Fatalf("requests = %d, want 1", *requests)
		}
	})

	t.Run("malformed bytes report blockers without panic", func(t *testing.T) {
		payload := []byte("not-an-archive-at-all")
		maintainer, _ := httpsArtifactFixture(t, httpsArtifactOK(payload))
		result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: target})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "ready" || result.Artifact == nil {
			t.Fatalf("result = %#v", result)
		}
		if result.Artifact.ArtifactType != "unknown" {
			t.Fatalf("artifact type = %q, want unknown", result.Artifact.ArtifactType)
		}
		found := false
		for _, blocker := range result.Artifact.Blockers {
			if blocker == "UNSUPPORTED_ARTIFACT" {
				found = true
			}
		}
		if !found {
			t.Fatalf("blockers = %#v, want UNSUPPORTED_ARTIFACT", result.Artifact.Blockers)
		}
		checkArtifactDigests(t, result, payload)
	})

	t.Run("unextractable gzip reports blockers without panic", func(t *testing.T) {
		var output bytes.Buffer
		compressed := gzip.NewWriter(&output)
		if _, err := compressed.Write([]byte("plain text, not tar")); err != nil {
			t.Fatal(err)
		}
		if err := compressed.Close(); err != nil {
			t.Fatal(err)
		}
		payload := output.Bytes()
		gzTarget := "https://download.example.test/release/fixture-linux-amd64.tar.gz"
		maintainer, _ := httpsArtifactFixture(t, httpsArtifactOK(payload))
		result, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: gzTarget})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "ready" || result.Artifact == nil {
			t.Fatalf("result = %#v", result)
		}
		if result.Artifact.ArtifactType != "tar.gz" {
			t.Fatalf("artifact type = %q, want tar.gz", result.Artifact.ArtifactType)
		}
		found := false
		for _, blocker := range result.Artifact.Blockers {
			if blocker == "UNSUPPORTED_ARTIFACT" {
				found = true
			}
		}
		if !found {
			t.Fatalf("blockers = %#v, want UNSUPPORTED_ARTIFACT", result.Artifact.Blockers)
		}
		checkArtifactDigests(t, result, payload)
	})
}

func TestInspectRegistryLocalFormsUnchanged(t *testing.T) {
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	maintainer := &Maintainer{layout: layout}

	root := t.TempDir()
	manifestPath := filepath.Join(root, "apps", "fixture", "manifest.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, []byte(onboardingValidManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	manifestResult, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: manifestPath})
	if err != nil {
		t.Fatal(err)
	}
	if manifestResult.Status != "ready" || manifestResult.Manifest == nil || !manifestResult.Manifest.Valid {
		t.Fatalf("manifest result = %#v", manifestResult)
	}

	directoryResult, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: root})
	if err != nil {
		t.Fatal(err)
	}
	if directoryResult.Status != "ready" || directoryResult.Directory == nil || directoryResult.Directory.Manifests != 1 {
		t.Fatalf("directory result = %#v", directoryResult)
	}

	missing := filepath.Join(root, "does-not-exist.yaml")
	if _, err := maintainer.InspectRegistry(context.Background(), RegistryInspectOptions{Target: missing}); err == nil {
		t.Fatal("missing local target unexpectedly succeeded")
	}
}
