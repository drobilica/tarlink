package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/registry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func registryRuntimeLayout(t *testing.T) filesystem.Layout {
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

func registryRuntimeArchive(t *testing.T, version string) []byte {
	t.Helper()
	manifest := `schema: 1
id: fixture
name: Fixture
summary: Registry fixture
homepage: https://example.com/
categories: [utilities]
platform: {os: linux, arch: amd64}
release:
  version: "` + version + `"
  url: https://example.com/fixture.tar.gz
  archive: tar.gz
  verification:
    algorithm: sha256
    digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    source: https://example.com/SHA256SUMS
application: {executable: fixture}
desktop: {enabled: false, categories: []}
`
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name string
		mode int64
		body []byte
	}{
		{name: "tarlink-registry-main/", mode: 0o755},
		{name: "tarlink-registry-main/apps/", mode: 0o755},
		{name: "tarlink-registry-main/apps/fixture/", mode: 0o755},
		{name: "tarlink-registry-main/apps/fixture/manifest.yaml", mode: 0o644, body: []byte(manifest)},
	}
	for _, entry := range entries {
		typeFlag := byte(tar.TypeDir)
		if entry.body != nil {
			typeFlag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: typeFlag, Size: int64(len(entry.body))}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) != 0 {
			if _, err := tarWriter.Write(entry.body); err != nil {
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

func registryRuntimeCore(t *testing.T, responder roundTripFunc) (*Core, *int) {
	t.Helper()
	layout := registryRuntimeLayout(t)
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return responder(request)
	})}
	client := &download.Client{HTTP: httpClient, RedirectLimit: 2}
	return &Core{
		layout: layout, installer: install.New(layout, client),
		syncer: &registry.Syncer{CacheRoot: filepath.Join(layout.Cache, "registry"), LocksRoot: layout.Locks, Client: client},
		now:    time.Now, registryMaxAge: registry.DefaultMaxAge,
		goos: "linux", goarch: "amd64",
	}, &requests
}

func registryResponse(payload []byte) roundTripFunc {
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

func TestCatalogAutomaticallyBootstrapsAndUsesFreshCache(t *testing.T) {
	core, requests := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "1.0" {
		t.Fatalf("initial Search() applications=%#v error=%v", applications, err)
	}
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatalf("cached Search() error = %v", err)
	}
	if *requests != 1 {
		t.Fatalf("registry requests = %d, want 1", *requests)
	}
}

func TestCatalogRefreshesStaleCache(t *testing.T) {
	payload := registryRuntimeArchive(t, "1.0")
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		return registryResponse(payload)(request)
	})
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	payload = registryRuntimeArchive(t, "2.0")
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	if err := os.Chtimes(generation, old, old); err != nil {
		t.Fatal(err)
	}
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("refreshed Search() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}

func TestCatalogUsesValidStaleCacheWhenOffline(t *testing.T) {
	offline := false
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		if offline {
			return nil, errors.New("offline")
		}
		return registryResponse(registryRuntimeArchive(t, "1.0"))(request)
	})
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	if err := os.Chtimes(generation, old, old); err != nil {
		t.Fatal(err)
	}
	offline = true
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "1.0" {
		t.Fatalf("offline Search() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}

func TestCatalogDoesNotHideCancellationBehindStaleCache(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if _, err := core.Search(context.Background(), "fixture"); err != nil {
		t.Fatal(err)
	}
	current, err := os.Readlink(filepath.Join(core.syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	generation := filepath.Join(core.syncer.CacheRoot, current)
	old := time.Now().Add(-registry.DefaultMaxAge - time.Hour)
	if err := os.Chtimes(generation, old, old); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := core.Search(ctx, "fixture"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Search() error = %v, want context cancellation", err)
	}
}

func TestCatalogFailsOfflineWithoutCache(t *testing.T) {
	core, _ := registryRuntimeCore(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})
	if _, err := core.Search(context.Background(), "fixture"); err == nil {
		t.Fatal("Search() unexpectedly succeeded without a registry")
	}
}

func TestExplicitRegistrySyncAlwaysFetches(t *testing.T) {
	core, requests := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests = %d, want 2", *requests)
	}
}
