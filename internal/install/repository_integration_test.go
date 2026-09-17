package install

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/drobilica/tarlink/internal/artifactrepo"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/ulikunitz/xz"
)

func TestStaticRepositoryAcquiresApplicationAndRuntime(t *testing.T) {
	layout := testLayout(t)
	repository := t.TempDir()
	if err := artifactrepo.Init(repository); err != nil {
		t.Fatal(err)
	}

	applicationBytes := fixtureArchive(t, "repository")
	runtimeBytes := repositoryRuntimeArchive(t)
	applicationDigest := sha256.Sum256(applicationBytes)
	runtimeDigest := sha256.Sum256(runtimeBytes)
	publish := func(data []byte, digest [32]byte) {
		t.Helper()
		if err := artifactrepo.Add(context.Background(), repository, "sha256", hex.EncodeToString(digest[:]), func(_ context.Context, _, _, _, destination string) error {
			return os.WriteFile(destination, data, 0o600)
		}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	publish(applicationBytes, applicationDigest)
	publish(runtimeBytes, runtimeDigest)

	runtimeReference := &manifest.RuntimeReference{ID: "steam-linux-runtime-4", Version: "4.0.20260805.254769"}
	runtimeValue := &manifest.Runtime{
		Schema: manifest.RuntimeSchemaV1, ID: runtimeReference.ID, Kind: manifest.RuntimeKindSteamLinuxRuntime,
		Version: runtimeReference.Version, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Artifact: manifest.RuntimeArtifact{
			URL: "https://upstream.invalid/runtime.tar.xz", Archive: "tar.xz",
			Verification: manifest.Verification{Algorithm: "sha256", Digest: hex.EncodeToString(runtimeDigest[:]), Source: "https://upstream.invalid/SHA256SUMS"},
		},
		Interface: manifest.RuntimeInterfaceValveV2,
	}
	release := manifest.Release{
		Channel: "stable", Version: "1.0", URL: "https://upstream.invalid/application.tar.gz", Archive: "tar.gz",
		Verification: manifest.Verification{Algorithm: "sha256", Digest: hex.EncodeToString(applicationDigest[:]), Source: "https://upstream.invalid/SHA256SUMS"},
		RuntimeRef:   runtimeReference, Runtime: runtimeValue,
	}
	item := &manifest.Manifest{
		Schema: manifest.SchemaV5, ID: "fixture-repository", Name: "Repository Fixture", Summary: "Repository Fixture",
		Homepage: "https://example.com/", Categories: []string{"utilities"}, Platform: manifest.Platform{OS: "linux", Arch: "amd64"},
		Release: release,
		ReleaseHistory: manifest.ReleaseHistory{
			DefaultChannel: "stable", Channels: map[string]manifest.ChannelHead{"stable": {Current: "1.0"}},
			Releases: []manifest.Release{{Channel: "stable", Version: "1.0", URL: release.URL, Archive: release.Archive, Verification: release.Verification, RuntimeRef: runtimeReference}},
		},
		Application: manifest.Application{Executables: []manifest.Executable{{Name: "run", Path: "bin/run", CreateBinLink: boolPointer(false)}}},
		RuntimeRef:  runtimeReference, Runtime: runtimeValue,
	}
	if err := item.Validate(); err != nil {
		t.Fatal(err)
	}

	client := &download.Client{HTTP: &http.Client{Transport: unavailableTransport{}}, Sources: []string{repository}}
	manager := New(layout, client)
	outcome, err := manager.InstallWithOptions(context.Background(), item, Options{Channel: "stable"}, nil)
	if err != nil {
		t.Fatalf("repository-backed install failed while upstream was unavailable: %v", err)
	}
	if outcome.State.Runtime == nil || outcome.State.Runtime.Artifact.Verification.Digest != hex.EncodeToString(runtimeDigest[:]) {
		t.Fatalf("runtime closure was not retained: %#v", outcome.State.Runtime)
	}
	runtimeFingerprint, err := outcome.State.Runtime.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	runtimePath, err := layout.RuntimePath(runtimeValue.ID, runtimeValue.Version, runtimeFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runtimePath, "_v2-entry-point")); err != nil {
		t.Fatalf("runtime was not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(layout.Apps, item.ID, "current", "bin", "run")); err != nil {
		t.Fatalf("application was not activated: %v", err)
	}
}

func repositoryRuntimeArchive(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	xzWriter, err := xz.NewWriter(&output)
	if err != nil {
		t.Fatal(err)
	}
	tarWriter := tar.NewWriter(xzWriter)
	entry := []byte("runtime fixture")
	for _, header := range []tar.Header{
		{Name: "SteamLinuxRuntime_4/", Typeflag: tar.TypeDir, Mode: 0o755},
		{Name: "SteamLinuxRuntime_4/_v2-entry-point", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(entry))},
	} {
		if err := tarWriter.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tarWriter.Write(entry); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type unavailableTransport struct{}

func (unavailableTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("upstream unavailable")
}

func boolPointer(value bool) *bool { return &value }
