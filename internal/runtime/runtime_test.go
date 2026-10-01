package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/ulikunitz/xz"
)

func runtimeArchiveBytes(t *testing.T, entries []tar.Header) []byte {
	t.Helper()
	var data bytes.Buffer
	writer, err := xz.NewWriter(&data)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(writer)
	for _, header := range entries {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write(make([]byte, header.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func runtimeArchive(t *testing.T, entries []tar.Header) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime.tar.xz")
	if err := os.WriteFile(path, runtimeArchiveBytes(t, entries), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openArchive(t *testing.T, path string) *os.File {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestExtractValveDeploymentAllowsContainedRelativeLink(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "SteamLinuxRuntime_4/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755}, {Name: "SteamLinuxRuntime_4/link", Typeflag: tar.TypeSymlink, Linkname: "_v2-entry-point"}})
	destination := t.TempDir()
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), destination, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateDeployment(filepath.Join(destination, "SteamLinuxRuntime_4")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractValveDeploymentRejectsEscapingLink(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755}, {Name: "SteamLinuxRuntime_4/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}})
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), t.TempDir(), nil); err == nil {
		t.Fatal("escaping symlink was accepted")
	}
}

func TestRuntimePathBounds(t *testing.T) {
	if _, err := runtimePath("SteamLinuxRuntime_4/" + strings.Repeat("nested/", maxRuntimeDepth) + "file"); err == nil {
		t.Fatal("runtime path exceeded depth limit")
	}
	if _, err := runtimePath("SteamLinuxRuntime_4/" + strings.Repeat("x", maxRuntimePath)); err == nil {
		t.Fatal("runtime path exceeded length limit")
	}
}

func TestExtractValveDeploymentAllowsSniperRoot(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{
		{Name: "SteamLinuxRuntime_sniper/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "SteamLinuxRuntime_sniper/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755},
	})
	destination := t.TempDir()
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), destination, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateDeployment(filepath.Join(destination, "SteamLinuxRuntime_sniper")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractValveDeploymentRejectsUnknownRoot(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{
		{Name: "SteamLinuxRuntime_5/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "SteamLinuxRuntime_5/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755},
	})
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), t.TempDir(), nil); err == nil {
		t.Fatal("unknown runtime root was accepted")
	}
}

func TestExtractValveDeploymentSniperLinkStaysWithinRoot(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{
		{Name: "SteamLinuxRuntime_sniper/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "SteamLinuxRuntime_sniper/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755},
		{Name: "SteamLinuxRuntime_sniper/link", Typeflag: tar.TypeSymlink, Linkname: "_v2-entry-point"},
	})
	destination := t.TempDir()
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), destination, nil); err != nil {
		t.Fatal(err)
	}
	if err := validateDeployment(filepath.Join(destination, "SteamLinuxRuntime_sniper")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractValveDeploymentSniperLinkEscapingRejected(t *testing.T) {
	archive := runtimeArchive(t, []tar.Header{
		{Name: "SteamLinuxRuntime_sniper/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "SteamLinuxRuntime_sniper/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755},
		{Name: "SteamLinuxRuntime_sniper/link", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
	})
	if err := extractValveDeployment(context.Background(), openArchive(t, archive), t.TempDir(), nil); err == nil {
		t.Fatal("escaping sniper symlink was accepted")
	}
}

func TestRuntimePathAdmittedRoots(t *testing.T) {
	for _, root := range []string{"SteamLinuxRuntime_4", "SteamLinuxRuntime_sniper"} {
		if _, err := runtimePath(root + "/_v2-entry-point"); err != nil {
			t.Fatalf("admitted root %q rejected: %v", root, err)
		}
	}
	for _, root := range []string{"SteamLinuxRuntime_3", "SteamLinuxRuntime_5", "runtime"} {
		if _, err := runtimePath(root + "/_v2-entry-point"); err == nil {
			t.Fatalf("non-admitted root %q accepted", root)
		}
	}
}

func TestGCRejectsNonCanonicalDeploymentName(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("runtime ownership tests require Linux path semantics")
	}
	layout := runtimeTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(layout.Runtimes, "steam-linux-runtime-4", "4.0", ".tarlink-runtime-not-a-digest")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := GC(layout); err == nil {
		t.Fatal("GC accepted a non-canonical deployment name")
	}
}

func TestGCRejectsUppercaseDeploymentName(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("runtime ownership tests require Linux path semantics")
	}
	layout := runtimeTestLayout(t)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(layout.Runtimes, "steam-linux-runtime-4", "4.0", ".tarlink-runtime-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := GC(layout); err == nil {
		t.Fatal("GC accepted an uppercase deployment name")
	}
}

func TestEnsureReusesCanonicalVerifiedArtifactCache(t *testing.T) {
	data := runtimeArchiveBytes(t, []tar.Header{
		{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755},
		{Name: "SteamLinuxRuntime_4/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755},
	})
	digest := sha256.Sum256(data)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write(data)
	}))
	defer server.Close()

	layout := runtimeTestLayout(t)
	client := &download.Client{HTTP: server.Client(), RedirectLimit: 2}
	runtimeFor := func(version string) *manifest.Runtime {
		return &manifest.Runtime{
			Schema: manifest.RuntimeSchemaV1, ID: "steam-linux-runtime-4", Kind: manifest.RuntimeKindSteamLinuxRuntime,
			Version: version, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
			Artifact: manifest.RuntimeArtifact{
				URL: server.URL, Archive: "tar.xz",
				Verification: manifest.Verification{Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Source: server.URL + "/SHA256SUMS"},
			},
			Interface: manifest.RuntimeInterfaceValveV2,
		}
	}
	if _, _, err := Ensure(context.Background(), layout, client, runtimeFor("4.0.1"), nil); err != nil {
		t.Fatalf("first Ensure() error = %v", err)
	}
	if _, _, err := Ensure(context.Background(), layout, client, runtimeFor("4.0.2"), nil); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("runtime requests = %d, want 1 (canonical cache reuse)", requests)
	}
}

func TestEnsureRealValveArtifact(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("Valve runtime preflight requires Linux")
	}
	archivePath := os.Getenv("TARLINK_REAL_VALVE_ARTIFACT")
	if archivePath == "" {
		t.Skip("set TARLINK_REAL_VALVE_ARTIFACT for the explicit Valve artifact preflight")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		t.Fatal(err)
	}
	layout := runtimeTestLayout(t)
	client := &download.Client{HTTP: &http.Client{Transport: roundTripFile{file: archive, size: info.Size()}}}
	value := &manifest.Runtime{Schema: manifest.RuntimeSchemaV1, ID: "steam-linux-runtime-4", Kind: manifest.RuntimeKindSteamLinuxRuntime, Version: "4.0.20260805.254769", Platform: manifest.Platform{OS: "linux", Arch: "amd64"}, Artifact: manifest.RuntimeArtifact{URL: "https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SteamLinuxRuntime_4.tar.xz", Archive: "tar.xz", Verification: manifest.Verification{Algorithm: "sha256", Digest: "3226d8234e7c0542ee767837832bfb1dad5e5e2dc944ec97eb221b437f6b9349", Source: "https://repo.steampowered.com/steamrt4/images/4.0.20260805.254769/SHA256SUMS"}}, Interface: manifest.RuntimeInterfaceValveV2}
	if _, _, err := Ensure(context.Background(), layout, client, value, nil); err != nil {
		t.Fatal(err)
	}
}

type roundTripFile struct {
	file *os.File
	size int64
}

func (r roundTripFile) RoundTrip(request *http.Request) (*http.Response, error) {
	if _, err := r.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(r.file), Header: make(http.Header), Request: request, ContentLength: r.size}, nil
}

func TestExtractValveDeploymentReportsExtractionProgress(t *testing.T) {
	payload := make([]byte, 3<<20)
	for i := range payload {
		payload[i] = byte(i)
	}
	var data bytes.Buffer
	writer, err := xz.NewWriter(&data)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(writer)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
		t.Fatal(err)
	}
	header := &tar.Header{Name: "SteamLinuxRuntime_4/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(payload))}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	type event struct {
		stage   string
		current int64
		total   int64
	}
	var events []event
	destination := t.TempDir()
	if err := extractValveDeployment(context.Background(), bytes.NewReader(data.Bytes()), destination, func(stage string, current, total int64) {
		events = append(events, event{stage: stage, current: current, total: total})
	}); err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("extraction progress events = %d, want at least 2", len(events))
	}
	var last int64 = -1
	sawAdvance := false
	for _, e := range events {
		if e.stage != "extracting" {
			t.Fatalf("extraction stage = %q, want extracting", e.stage)
		}
		if e.current < last {
			t.Fatalf("extraction progress regressed: %d after %d", e.current, last)
		}
		if e.current > last {
			sawAdvance = true
		}
		last = e.current
	}
	if !sawAdvance || last != int64(len(payload)) {
		t.Fatalf("extraction did not advance to payload size: events=%v last=%d want=%d", events, last, len(payload))
	}
}

func runtimeTestLayout(t *testing.T) filesystem.Layout {
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
	return layout
}
