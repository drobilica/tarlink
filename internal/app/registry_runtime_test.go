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
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/install"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/state"
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
	return registryRuntimeArchivePlatforms(t, version, false)
}

func registryRuntimeArchivePlatforms(t *testing.T, version string, arm64 bool) []byte {
	return registryRuntimeArchivePlatformVersions(t, version, version+"-arm64", arm64)
}

func registryRuntimeArchivePlatformVersions(t *testing.T, amd64Version, arm64Version string, arm64 bool) []byte {
	t.Helper()
	manifest := `schema: 2
id: fixture
name: Fixture
summary: Registry fixture
homepage: https://example.com/
categories: [utilities]
platform: {os: linux, arch: amd64}
release:
  default-channel: stable
  channels:
    stable: {current: "` + amd64Version + `"}
  releases:
    - channel: stable
      version: "` + amd64Version + `"
      url: https://example.com/fixture.tar.gz
      archive: tar.gz
      verification:
        algorithm: sha256
        digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
        source: https://example.com/SHA256SUMS
application: {executables: [{name: fixture, path: fixture}]}
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
		{name: "tarlink-registry-main/apps/fixture/linux-amd64.yaml", mode: 0o644, body: []byte(manifest)},
	}
	if arm64 {
		// The channel head and release entry must describe the same
		// platform-specific release. Replacing every occurrence also updates
		// the channel's current pointer; leaving the amd64 head here makes the
		// fixture invalid under the v2 duplicate/current-head checks.
		armManifest := strings.ReplaceAll(manifest, `"`+amd64Version+`"`, `"`+arm64Version+`"`)
		armManifest = strings.Replace(armManifest, "arch: amd64", "arch: arm64", 1)
		armManifest = strings.Replace(armManifest, "name: fixture, path: fixture", "name: fixture-arm64, path: fixture", 1)
		entries = append(entries, struct {
			name string
			mode int64
			body []byte
		}{name: "tarlink-registry-main/apps/fixture/linux-arm64.yaml", mode: 0o644, body: []byte(armManifest)})
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

func TestCatalogRejectsCorruptStaleCacheWhenOffline(t *testing.T) {
	tests := map[string]func(*testing.T, string, string){
		"current pointer": func(t *testing.T, cacheRoot, generation string) {
			current := filepath.Join(cacheRoot, "current")
			if err := os.Remove(current); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("../outside", current); err != nil {
				t.Fatal(err)
			}
		},
		"generation": func(t *testing.T, _, generation string) {
			if err := os.RemoveAll(filepath.Join(generation, "apps")); err != nil {
				t.Fatal(err)
			}
		},
		"manifest": func(t *testing.T, _, generation string) {
			manifestPath := filepath.Join(generation, "apps", "fixture", "linux-amd64.yaml")
			if err := os.WriteFile(manifestPath, []byte("not a manifest"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
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
			corrupt(t, core.syncer.CacheRoot, generation)
			offline = true
			applications, err := core.Search(context.Background(), "fixture")
			if err == nil || applications != nil {
				t.Fatalf("corrupt offline Search() applications=%#v error=%v", applications, err)
			}
			if CodeOf(err) != CodeNetwork {
				t.Fatalf("corrupt offline Search() code=%q error=%v", CodeOf(err), err)
			}
			if *requests != 2 {
				t.Fatalf("registry requests = %d, want 2", *requests)
			}
		})
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

func TestSearchUsesExactRuntimePlatformVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchivePlatformVersions(t, "1.0", "2.0", true)))
	core.goarch = "arm64"
	applications, err := core.Search(context.Background(), "fixture")
	if err != nil || len(applications) != 1 || applications[0].ID != "fixture" || applications[0].Name != "Fixture" || applications[0].Summary != "Registry fixture" || applications[0].Homepage != "https://example.com/" || len(applications[0].Categories) != 1 || applications[0].Categories[0] != "utilities" || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("arm64 Search() applications=%#v error=%v", applications, err)
	}
	info, err := core.Info(context.Background(), "fixture")
	if err != nil || info.ID != "fixture" || info.Name != "Fixture" || info.Summary != "Registry fixture" || info.RegistryVersion != "2.0" {
		t.Fatalf("arm64 Info() application=%#v error=%v", info, err)
	}
	catalog, err := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	if err != nil {
		t.Fatal(err)
	}
	item, err := catalog.ManifestForPlatform("fixture", "linux", "arm64")
	if err != nil || item.Release.Version != "2.0" || item.Application.Executables[0].Name != "fixture-arm64" {
		t.Fatalf("arm64 manifest=%#v error=%v", item, err)
	}
}

func TestResolveMissingVariantReturnsTypedUnsupportedPlatform(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	core.goarch = "arm64"
	_, err := core.Info(context.Background(), "fixture")
	if CodeOf(err) != CodeUnsupportedPlatform {
		t.Fatalf("Info() code = %q, error = %v", CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "Fixture is not available for linux/arm64") {
		t.Fatalf("Info() error = %v", err)
	}
}

func TestListDoesNotClaimRegistryDataWithoutRuntimeVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchive(t, "1.0")))
	if err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{
		Schema: state.Schema, App: "fixture", Current: "0.9", Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}},
		Integration: state.Integration{
			Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	core.goarch = "arm64"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].ID != "fixture" || applications[0].Name != "fixture" || applications[0].InstalledVersion != "0.9" || applications[0].RegistryVersion != "" || applications[0].Summary != "" || applications[0].Homepage != "" || len(applications[0].Categories) != 0 || applications[0].UpdateAvailable {
		t.Fatalf("List() applications=%#v error=%v", applications, err)
	}
}

func TestListPrefersExactRuntimeVariant(t *testing.T) {
	core, _ := registryRuntimeCore(t, registryResponse(registryRuntimeArchivePlatformVersions(t, "1.0", "2.0", true)))
	if err := core.SyncRegistry(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{
		Schema: state.Schema, App: "fixture", Current: "1.5", Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}},
		Integration: state.Integration{
			Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	core.goarch = "arm64"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" || !applications[0].UpdateAvailable {
		t.Fatalf("arm64 List() applications=%#v error=%v", applications, err)
	}
}

func TestListUsesSharedRegistryFreshnessPolicy(t *testing.T) {
	version := "1.0"
	core, requests := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		return registryResponse(registryRuntimeArchive(t, version))(request)
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
	if err := state.Write(filepath.Join(core.layout.States, "fixture.json"), state.State{Schema: state.Schema, App: "fixture", Current: "0.9", Channel: "stable", Artifact: "tar.gz", Executables: []state.Executable{{Name: "fixture", Path: "fixture"}}, Integration: state.Integration{Executables: []state.ExecutableIntegration{{Name: "fixture", Path: "fixture", Link: filepath.Join(core.layout.Bin, "fixture"), Target: filepath.Join(core.layout.Apps, "fixture", "current", "fixture")}}}}); err != nil {
		t.Fatal(err)
	}
	version = "2.0"
	applications, err := core.List(context.Background())
	if err != nil || len(applications) != 1 || applications[0].RegistryVersion != "2.0" {
		t.Fatalf("List() applications=%#v error=%v", applications, err)
	}
	if *requests != 2 {
		t.Fatalf("registry requests=%d, want 2", *requests)
	}
}
