package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
)

func TestRegistryUpdateChecksAllPlatformsAndPreservesTree(t *testing.T) {
	root := t.TempDir()
	// Godot is deliberately available only on arm64. A maintainer's host must
	// not remove it from the scan. Tiled has different approved platform heads.
	for _, id := range []string{"godot", "tiled", "foo", "xemu"} {
		data := strings.ReplaceAll(onboardingValidManifest, "foo", id)
		data = strings.Replace(data, "name: Foo", "name: "+id, 1)
		if id == "godot" {
			data = strings.ReplaceAll(data, "linux-amd64", "linux-arm64")
		}
		if id == "tiled" {
			data = strings.Replace(data, "application:", `        linux-arm64:
          url: https://example.invalid/tiled-arm64-old.zip
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/tiled-arm64-old.zip
application:`, 1)
			data = strings.Replace(data, "application:", `    - version: 2.0.0
      artifacts:
        linux-arm64:
          url: https://example.invalid/tiled-arm64.zip
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://example.invalid/tiled-arm64.zip
application:`, 1)
		}
		if id == "xemu" {
			data = "enabled: false\n" + data
		}
		writeOnboardingManifest(t, filepath.Join(root, "apps", id, "manifest.yaml"), data)
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	before := registryEditSnapshot(t, root)
	calls := 0
	client := &freshness.Client{HTTP: &http.Client{Transport: maintainerRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || strings.Contains(r.URL.Path, "xemu") || strings.Contains(r.URL.Path, "foo") {
			t.Fatalf("unexpected network source %s", r.URL)
		}
		return maintainerResponse(http.StatusOK, `[
{"tag_name":"v2.0.0","html_url":"https://github.com/example/project/releases/tag/v2.0.0"},
{"tag_name":"v1.2.3"},
{"tag_name":"v3.0.0","prerelease":true},
{"tag_name":"v4.0.0","draft":true}]`), nil
	})}}
	report, err := checkRegistryUpdates(context.Background(), catalog, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Results) != 5 || calls != 3 || report.SchemaVersion != 1 || report.Revision != catalog.Revision {
		t.Fatalf("report=%+v calls=%d", report, calls)
	}
	want := []string{"foo/linux-amd64/skipped", "godot/linux-arm64/candidates", "tiled/linux-amd64/candidates", "tiled/linux-arm64/no-candidates", "xemu/linux-amd64/skipped"}
	var got []string
	for _, result := range report.Results {
		got = append(got, result.App+"/"+result.Platform+"/"+result.Status)
		if result.Candidates == nil {
			t.Fatal("nil candidates")
		}
		for _, candidate := range result.Candidates {
			if candidate.Channel != "stable" || candidate.Prerelease || candidate.Version == "3.0.0" || candidate.Version == "4.0.0" {
				t.Fatalf("invalid candidate %+v", candidate)
			}
		}
		if result.App == "tiled" && result.Platform == "linux-arm64" && len(result.Candidates) != 0 {
			t.Fatalf("platform-specific approvals lost: %+v", result)
		}
	}
	if !reflect.DeepEqual(got, want) || report.Results[0].Reason != "no_approved_upstream" || report.Results[4].Reason != "disabled" {
		t.Fatalf("results=%+v want=%v", report.Results, want)
	}
	if !reflect.DeepEqual(before, registryEditSnapshot(t, root)) {
		t.Fatal("scan modified registry files")
	}
}

func TestRegistryUpdateContinuesAfterFailureAndSupportsCancellation(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"godot", "tiled"} {
		data := strings.ReplaceAll(onboardingValidManifest, "foo", id)
		writeOnboardingManifest(t, filepath.Join(root, "apps", id, "manifest.yaml"), strings.Replace(data, "name: Foo", "name: "+id, 1))
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"http", "json", "size", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client := &freshness.Client{HTTP: &http.Client{Transport: maintainerRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if strings.Contains(r.URL.Path, "godot") {
					switch failure {
					case "http":
						return maintainerResponse(http.StatusForbidden, `{}`), nil
					case "json":
						return maintainerResponse(http.StatusOK, `invalid`), nil
					case "size":
						return maintainerResponse(http.StatusOK, strings.Repeat(" ", (4<<20)+1)), nil
					case "cancel":
						cancel()
						return nil, ctx.Err()
					}
				}
				return maintainerResponse(http.StatusOK, `[{"tag_name":"v1.2.3"}]`), nil
			})}}
			report, err := checkRegistryUpdates(ctx, catalog, client)
			if err == nil || len(report.Results) == 0 || report.Results[0].Status != "error" || report.Results[0].Error == "" {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if failure == "cancel" {
				if calls != 1 || !errors.Is(err, context.Canceled) {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if calls != 2 || len(report.Results) != 2 || report.Results[1].Status != "no-candidates" {
				t.Fatalf("scan did not continue: calls=%d report=%+v", calls, report)
			}
		})
	}
}

func TestUpdateRegistryRefreshesOfficialSnapshotWithoutInstalling(t *testing.T) {
	archive := repositoryServiceArchive(t, map[string][]byte{"godot": []byte("not downloaded")})
	failRefresh := false
	apiCalls := 0
	registryCalls := 0
	core, _ := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Host {
		case "codeload.github.com":
			registryCalls++
			if failRefresh {
				return nil, errors.New("refresh unavailable")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header), ContentLength: int64(len(archive))}, nil
		case "api.github.com":
			apiCalls++
			return maintainerResponse(http.StatusOK, `[{"tag_name":"v2.0"}]`), nil
		default:
			t.Fatalf("unexpected artifact request %s", request.URL)
			return nil, errors.New("unexpected request")
		}
	})
	// The scan must cover the registry's Linux platform even on another host.
	core.goos, core.goarch = "darwin", "arm64"
	for i := 0; i < 2; i++ {
		report, err := core.UpdateRegistry(context.Background(), nil)
		if err != nil || len(report.Results) != 1 || report.Results[0].Status != "candidates" || report.CheckedAt.IsZero() {
			t.Fatalf("report=%+v err=%v", report, err)
		}
	}
	if registryCalls != 2 || apiCalls != 2 {
		t.Fatalf("syncs=%d API calls=%d", registryCalls, apiCalls)
	}
	if states, err := os.ReadDir(core.layout.States); err != nil && !errors.Is(err, os.ErrNotExist) || len(states) > 0 {
		t.Fatalf("scan wrote application state: %v %v", states, err)
	}
	failRefresh = true
	if report, err := core.UpdateRegistry(context.Background(), nil); err == nil || len(report.Results) != 0 || apiCalls != 2 {
		t.Fatalf("failed refresh used stale snapshot: report=%+v err=%v", report, err)
	}
	core.EnableOffline()
	if _, err := core.UpdateRegistry(context.Background(), nil); !errors.Is(err, download.ErrOffline) {
		t.Fatalf("offline error=%v", err)
	}
}

func TestRegistryUpdateOrdersChannelsAndRetainsPartialCandidates(t *testing.T) {
	root := registryEditFixture(t)
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		t.Fatal(err)
	}
	item := catalog.Variants["foo"][manifest.Platform{OS: "linux", Arch: "amd64"}]
	delete(catalog.Variants, "foo")
	catalog.Variants["godot"] = map[manifest.Platform]*manifest.Manifest{item.Platform: item}
	item.ReleaseHistory.Channels["nightly"] = manifest.ChannelHead{Current: "1.2.3"}
	calls := 0
	client := &freshness.Client{HTTP: &http.Client{Transport: maintainerRoundTrip(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return maintainerResponse(http.StatusForbidden, ""), nil
		}
		return maintainerResponse(http.StatusOK, `[{"tag_name":"v3.0.0","prerelease":true}]`), nil
	})}}
	report, err := checkRegistryUpdates(context.Background(), catalog, client)
	if err == nil || len(report.Results) != 1 || report.Results[0].Status != "error" || len(report.Results[0].Candidates) != 1 || report.Results[0].Candidates[0].Channel != "nightly" {
		t.Fatalf("partial channel result lost: %+v err=%v", report, err)
	}
}

func TestUpdateRegistryRejectsUpstreamRedirectsAndBoundsRequests(t *testing.T) {
	archive := repositoryServiceArchive(t, map[string][]byte{"godot": []byte("unused")})
	apiCalls := 0
	core, _ := registryRuntimeCore(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "codeload.github.com" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(archive)), Header: make(http.Header), ContentLength: int64(len(archive))}, nil
		}
		apiCalls++
		if request.URL.Host != "api.github.com" || request.URL.Scheme != "https" {
			t.Fatalf("followed untrusted redirect: %s", request.URL)
		}
		if deadline, ok := request.Context().Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("upstream request is not bounded by 30 seconds")
		}
		response := maintainerResponse(http.StatusFound, "")
		response.Header.Set("Location", "http://untrusted.invalid/releases")
		return response, nil
	})
	report, err := core.UpdateRegistry(context.Background(), nil)
	if err == nil || apiCalls != 1 || len(report.Results) != 1 || report.Results[0].Status != "error" {
		t.Fatalf("report=%+v calls=%d err=%v", report, apiCalls, err)
	}
	if core.syncer.Client.HTTP.CheckRedirect != nil || core.syncer.Client.HTTP.Timeout != 0 {
		t.Fatal("scan changed the shared registry download client")
	}
}
