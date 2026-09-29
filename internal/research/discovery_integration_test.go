package research

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type discoveryRegistryFixture struct{ id, provider, repo, release string }

func discoveryFixture(t *testing.T, apps []quiverApp, platforms []quiverPlatformEntry, registryEntries []discoveryRegistryFixture) DiscoveryOptions {
	t.Helper()
	root := t.TempDir()
	catalog, registryRoot := filepath.Join(root, "catalog"), filepath.Join(root, "registry")
	if err := os.MkdirAll(filepath.Join(catalog, "community-app-catalog"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFixtureJSON(t, filepath.Join(catalog, "index.json"), json.RawMessage(`{"version":2,"lists":[{"id":"1"},{"id":"2"},{"id":"3"},{"id":"4"}],"platformMetadataUrl":"https://example.invalid/ignored"}`))
	writeFixtureJSON(t, filepath.Join(catalog, "platform-index.json"), quiverPlatform{FormatRevision: 1, Entries: platforms})
	for _, group := range []string{"Nintendo", "PlayStation", "Xbox", "OtherPlatforms"} {
		groupApps := []quiverApp{}
		if group == "OtherPlatforms" {
			groupApps = apps
		}
		writeFixtureJSON(t, filepath.Join(catalog, "community-app-catalog", group+".json"), struct {
			Name string      `json:"name"`
			Apps []quiverApp `json:"apps"`
		}{group, groupApps})
	}
	for _, entry := range registryEntries {
		dir := filepath.Join(registryRoot, "apps", entry.id)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		base := "https://" + entry.provider + ".com/" + entry.repo
		releaseURL := base + "/releases/download/" + entry.release + "/Game-linux-amd64.AppImage"
		if entry.provider == "gitlab" {
			releaseURL = base + "/-/releases/" + entry.release + "/downloads/Game-linux-amd64.AppImage"
		}
		// Digest is synthetic fixture data; no artifact is fetched.
		text := fmt.Sprintf(`schema: 5
id: %s
name: %s
summary: Fixture game
homepage: %s
categories: [games]
requirements: [original-game-data]
release:
  current: %q
  verification: {algorithm: sha256}
  releases:
    - version: %q
      artifacts:
        linux-amd64:
          archive: appimage
          url: %s
          verification: {digest: %s, source: %s}
application:
  executable: {path: appimage, create-bin-link: false}
desktop:
  categories: [Game]
`, entry.id, entry.id, base, entry.release, entry.release, releaseURL, strings.Repeat("a", 64), base)
		if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return DiscoveryOptions{Catalog: "quiver:" + catalog, Registry: registryRoot, ResolveIdentity: func(_ context.Context, _ string, repo string) (string, error) { return repo, nil }}
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func fixtureApp(id, provider, repo string) quiverApp {
	return quiverApp{CatalogID: id, Name: id, RepositorySource: provider, Repository: repo, AppIconURL: "https://example.invalid/never-fetch.png"}
}

func fixturePlatform(provider, repo, release string, assets ...string) quiverPlatformEntry {
	return quiverPlatformEntry{Provider: provider, Repository: repo, ReleaseTag: release, SelectionRevision: 1, AssetNames: assets}
}

func TestDiscoveryMissingPlatformProviderRemainsAmbiguous(t *testing.T) {
	opts := discoveryFixture(t, []quiverApp{fixtureApp("game", "github", "owner/game")}, []quiverPlatformEntry{fixturePlatform("", "owner/game", "v1", "Game-linux-amd64.AppImage")}, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
	report, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	entry := report.Entries[0]
	if entry.Status != "ambiguous" || len(entry.RegistryMatches) != 1 || len(entry.Preferred.Assets) != 0 {
		t.Fatalf("missing provider produced confident selection: %+v", entry)
	}
}

func TestDiscoveryConflictingArchitectureEvidence(t *testing.T) {
	for _, test := range []struct {
		name, status, architectures string
		assets, preferred           []string
	}{
		{"amd64 and RISC-V", "ambiguous", "amd64,riscv64", []string{"Game-linux-amd64-riscv64.AppImage"}, nil},
		{"arm64 and i386", "ambiguous", "arm64,i386", []string{"Game-linux-arm64-i386.AppImage"}, nil},
		{"valid variant retained", "ambiguous", "amd64,arm64,riscv64", []string{"Game-linux-amd64-riscv64.AppImage", "Game-linux-arm64.AppImage"}, []string{"Game-linux-arm64.AppImage"}},
		{"both supported", "new", "amd64,arm64", []string{"Game-linux-amd64-arm64.AppImage"}, []string{"Game-linux-amd64-arm64.AppImage"}},
		{"only unsupported", "unsupported", "i386,riscv64", []string{"Game-linux-i386-riscv64.AppImage"}, nil},
		{"unknown remains unverified", "new", "unknown", []string{"Game.AppImage"}, []string{"Game.AppImage"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := discoveryFixture(t, []quiverApp{fixtureApp("game", "github", "owner/game")}, []quiverPlatformEntry{fixturePlatform("github", "owner/game", "v1", test.assets...)}, []discoveryRegistryFixture{{"other", "github", "owner/other", "v1"}})
			report, err := Discover(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			entry := report.Entries[0]
			if entry.Status != test.status || strings.Join(entry.Architectures, ",") != test.architectures || strings.Join(entry.Preferred.Assets, ",") != strings.Join(test.preferred, ",") || len(entry.LinuxAssets) != len(test.assets) {
				t.Fatalf("architecture evidence lost or classified unsafely: %+v", entry)
			}
			if test.status == "ambiguous" && !strings.Contains(strings.Join(entry.Reasons, ","), "conflicting architecture markers") {
				t.Fatalf("missing ambiguity reason: %+v", entry)
			}
		})
	}
}

func TestDiscoveryIdentityMatchingIsLazyMemoizedAndProviderSpecific(t *testing.T) {
	for _, test := range []struct {
		name      string
		apps      []quiverApp
		registry  []discoveryRegistryFixture
		canonical map[string]string
		fail      bool
		statuses  []string
		matches   []int
		lookups   map[string]int
	}{
		{"exact multiple IDs", []quiverApp{fixtureApp("a", "github", "OWNER/Game.git")}, []discoveryRegistryFixture{{"first", "github", "owner/game", "v1"}, {"second", "github", "owner/game", "v1"}}, nil, false, []string{"registered-same"}, []int{2}, map[string]int{}},
		{"catalog old name", []quiverApp{fixtureApp("a", "github", "owner/old")}, []discoveryRegistryFixture{{"game", "github", "owner/new", "v1"}}, map[string]string{"github:owner/old": "https://github.com/OWNER/New.git"}, false, []string{"registered-same"}, []int{1}, map[string]int{"github:owner/old": 1}},
		{"registry old name", []quiverApp{fixtureApp("a", "github", "owner/new")}, []discoveryRegistryFixture{{"game", "github", "owner/old", "v1"}}, map[string]string{"github:owner/old": "owner/new"}, false, []string{"registered-same"}, []int{1}, map[string]int{"github:owner/new": 1, "github:owner/old": 1}},
		{"all canonical aliases", []quiverApp{fixtureApp("a", "github", "owner/new")}, []discoveryRegistryFixture{{"first", "github", "owner/old-a", "v1"}, {"second", "github", "owner/old-b", "v1"}}, map[string]string{"github:owner/old-a": "owner/new", "github:owner/old-b": "owner/new"}, false, []string{"registered-same"}, []int{2}, map[string]int{"github:owner/new": 1, "github:owner/old-a": 1, "github:owner/old-b": 1}},
		{"provider distinction", []quiverApp{fixtureApp("a", "github", "owner/game")}, []discoveryRegistryFixture{{"game", "gitlab", "owner/game", "v1"}}, nil, false, []string{"new"}, []int{0}, map[string]int{"github:owner/game": 1}},
		{"GitLab namespace", []quiverApp{fixtureApp("a", "gitlab", "https://gitlab.com/Group/Sub/Game.git")}, []discoveryRegistryFixture{{"game", "gitlab", "group/sub/game", "v1"}}, nil, false, []string{"registered-same"}, []int{1}, map[string]int{}},
		{"exact repos not queried for unrelated lead", []quiverApp{fixtureApp("a", "github", "owner/known"), fixtureApp("b", "github", "owner/lead")}, []discoveryRegistryFixture{{"known", "github", "owner/known", "v1"}, {"orphan", "github", "owner/orphan", "v1"}}, nil, false, []string{"registered-same", "new"}, []int{1, 0}, map[string]int{"github:owner/lead": 1, "github:owner/orphan": 1}},
		{"registry lookup memoized", []quiverApp{fixtureApp("a", "github", "owner/one"), fixtureApp("b", "github", "owner/two")}, []discoveryRegistryFixture{{"orphan", "github", "owner/orphan", "v1"}}, nil, false, []string{"new", "new"}, []int{0, 0}, map[string]int{"github:owner/one": 1, "github:owner/two": 1, "github:owner/orphan": 1}},
		{"failed lookup memoized", []quiverApp{fixtureApp("a", "github", "owner/lead"), fixtureApp("b", "github", "owner/lead")}, []discoveryRegistryFixture{{"orphan", "github", "owner/orphan", "v1"}}, nil, true, []string{"ambiguous", "ambiguous"}, []int{0, 0}, map[string]int{"github:owner/lead": 1, "github:owner/orphan": 1}},
		{"invalid canonical identity", []quiverApp{fixtureApp("a", "github", "owner/lead")}, []discoveryRegistryFixture{{"orphan", "github", "owner/orphan", "v1"}}, map[string]string{"github:owner/lead": "https://evil.invalid/owner/lead"}, false, []string{"ambiguous"}, []int{0}, map[string]int{"github:owner/lead": 1, "github:owner/orphan": 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			platforms := []quiverPlatformEntry{}
			seen := map[string]bool{}
			for _, app := range test.apps {
				repo, err := normalizeRepository(app.RepositorySource, app.Repository)
				if err != nil {
					t.Fatal(err)
				}
				if !seen[app.RepositorySource+repo] {
					platforms = append(platforms, fixturePlatform(app.RepositorySource, repo, "v1", "Game.AppImage"))
					seen[app.RepositorySource+repo] = true
				}
			}
			opts := discoveryFixture(t, test.apps, platforms, test.registry)
			calls := map[string]int{}
			opts.ResolveIdentity = func(_ context.Context, provider, repo string) (string, error) {
				key := provider + ":" + repo
				calls[key]++
				if test.fail {
					return "", errors.New("injected lookup failure")
				}
				if canonical, ok := test.canonical[key]; ok {
					return canonical, nil
				}
				return repo, nil
			}
			report, err := Discover(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateDiscoveryReport(report); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, test.lookups) {
				t.Fatalf("lookups=%v want=%v", calls, test.lookups)
			}
			if len(report.Entries) != len(test.statuses) {
				t.Fatalf("entries=%+v", report.Entries)
			}
			for i, entry := range report.Entries {
				if entry.Status != test.statuses[i] || len(entry.RegistryMatches) != test.matches[i] {
					t.Fatalf("entry=%+v", entry)
				}
			}
		})
	}
}

func TestDiscoveryGitHubRenameRedirectRetriesWithoutArtifactRequests(t *testing.T) {
	opts := discoveryFixture(t, []quiverApp{fixtureApp("a", "github", "owner/old")}, []quiverPlatformEntry{fixturePlatform("github", "owner/old", "v1", "Game.AppImage")}, []discoveryRegistryFixture{{"game", "github", "owner/new", "v1"}})
	opts.ResolveIdentity = nil
	var requests []string
	opts.HTTP = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		switch req.URL.String() {
		case "https://api.github.com/repos/owner/old":
			response := jsonResponse(http.StatusMovedPermanently, "")
			response.Header.Set("Location", "https://api.github.com/repos/owner/new")
			return response, nil
		case "https://api.github.com/repos/owner/new":
			return jsonResponse(http.StatusOK, `{"full_name":"Owner/New"}`), nil
		default:
			t.Errorf("unexpected request (artifact/icon download): %s", req.URL)
			return nil, errors.New("unexpected request")
		}
	})}
	report, err := Discover(context.Background(), opts)
	if err != nil || len(report.Entries) != 1 || report.Entries[0].Status != "registered-same" || len(requests) != 2 {
		t.Fatalf("report=%+v requests=%v err=%v", report, requests, err)
	}
}

func TestDiscoverySharedApplicationsRetainSelectionsAndAllMatchesBeforeFiltering(t *testing.T) {
	first, second := fixtureApp("a", "github", "owner/shared"), fixtureApp("b", "github", "owner/shared")
	first.ReleaseAssetFilter, second.ReleaseAssetFilter = "FireRed", "LeafGreen"
	assets := []string{"FireRed-linux-amd64.AppImage", "FireRed-linux-SteamDeck.AppImage", "LeafGreen-linux-arm64.AppImage", "FireRed-linux-amd64.tar.gz", "LeafGreen-linux-arm64.zip"}
	opts := discoveryFixture(t, []quiverApp{second, first}, []quiverPlatformEntry{fixturePlatform("github", "owner/shared", "v1", assets...)}, []discoveryRegistryFixture{{"red", "github", "owner/shared", "v1"}, {"green", "github", "owner/shared", "v2"}})
	opts.Changed = true
	report, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Entries) != 2 || report.Summary.Ambiguous != 2 || !report.Changed {
		t.Fatalf("report=%+v", report)
	}
	for _, entry := range report.Entries {
		if entry.Status != "ambiguous" || len(entry.RegistryMatches) != 2 || len(entry.Alternatives) != 1 || entry.ReleaseAssetFilter == "" {
			t.Fatalf("entry=%+v", entry)
		}
		for _, asset := range entry.Preferred.Assets {
			if !strings.Contains(asset, entry.ReleaseAssetFilter) {
				t.Fatalf("wrong selected asset: %+v", entry)
			}
		}
	}
	if len(report.Entries[0].Preferred.Assets) != 2 || len(report.Entries[1].Preferred.Assets) != 1 {
		t.Fatalf("variants dropped: %+v", report.Entries)
	}
}

func TestDiscoveryReportsStayDeterministicAcrossInputPermutations(t *testing.T) {
	apps := []quiverApp{fixtureApp("b", "github", "owner/game"), fixtureApp("a", "github", "owner/game")}
	apps[0].Name = "A \\| <img> [link](https://bad.invalid) *bold*\x1b\u009b\u202e"
	platforms := []quiverPlatformEntry{
		fixturePlatform("github", "owner/game", "v2", "region-linux-arm64.AppImage", "profile-linux-amd64.AppImage", "Game-linux-amd64.tar.xz", "Game-linux-arm64.zip"),
		fixturePlatform("github", "owner/game", "v1", "Game-linux-amd64.tar.gz"),
	}
	opts := discoveryFixture(t, apps, platforms, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
	before, err := os.ReadFile(filepath.Join(opts.Registry, "apps/game/manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	apps[0], apps[1] = apps[1], apps[0]
	platforms[0], platforms[1] = platforms[1], platforms[0]
	for i := range platforms {
		for l, r := 0, len(platforms[i].AssetNames)-1; l < r; l, r = l+1, r-1 {
			platforms[i].AssetNames[l], platforms[i].AssetNames[r] = platforms[i].AssetNames[r], platforms[i].AssetNames[l]
		}
	}
	root := strings.TrimPrefix(opts.Catalog, "quiver:")
	writeFixtureJSON(t, filepath.Join(root, "community-app-catalog/OtherPlatforms.json"), map[string]any{"name": "OtherPlatforms", "apps": apps})
	writeFixtureJSON(t, filepath.Join(root, "platform-index.json"), quiverPlatform{FormatRevision: 1, Entries: platforms})
	second, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Fatalf("JSON depends on input ordering:\n%s\n%s", a, b)
	}
	for _, markdown := range []bool{false, true} {
		var a, b bytes.Buffer
		if err := RenderDiscovery(&a, first, markdown); err != nil {
			t.Fatal(err)
		}
		if err := RenderDiscovery(&b, second, markdown); err != nil {
			t.Fatal(err)
		}
		if a.String() != b.String() || strings.ContainsAny(a.String(), "\x1b\u009b\u202e") {
			t.Fatalf("unsafe or nondeterministic rendering: %q", a.String())
		}
		if markdown && (!strings.Contains(a.String(), `\\\|`) || strings.Contains(a.String(), "<img>")) {
			t.Fatalf("external Markdown was not escaped: %q", a.String())
		}
	}
	for _, entry := range first.Entries {
		if entry.CatalogRelease != "" || !reflect.DeepEqual(entry.CatalogReleases, []string{"v1", "v2"}) || entry.Status != "ambiguous" {
			t.Fatalf("conflicting releases misrepresented: %+v", entry)
		}
	}
	after, err := os.ReadFile(filepath.Join(opts.Registry, "apps/game/manifest.yaml"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("registry was modified")
	}
}

func TestDiscoveryChangedFiltersTheSuppliedSnapshotAndCountsEmittedEntries(t *testing.T) {
	apps := []quiverApp{}
	platforms := []quiverPlatformEntry{}
	for _, item := range []struct{ id, release, asset string }{
		{"same", "v1", "Game.AppImage"}, {"diff", "1", "Game.AppImage"}, {"new", "v1", "Game.AppImage"},
		{"unsupported", "v1", "Game-linux-i386.zip"}, {"no-linux", "v1", "release.zip"}, {"missing", "", "Game.AppImage"},
	} {
		apps = append(apps, fixtureApp(item.id, "github", "owner/"+item.id))
		platforms = append(platforms, fixturePlatform("github", "owner/"+item.id, item.release, item.asset))
	}
	opts := discoveryFixture(t, apps, platforms, []discoveryRegistryFixture{{"same", "github", "owner/same", "v1"}, {"diff", "github", "owner/diff", "v1"}})
	all, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if all.Summary != (DiscoverySummary{New: 1, ReleaseDiff: 1, RegisteredSame: 1, Unsupported: 1, Ambiguous: 1, NoLinux: 1}) {
		t.Fatalf("summary=%+v entries=%+v", all.Summary, all.Entries)
	}
	opts.Changed = true
	filtered, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Entries) != 4 || filtered.Summary.RegisteredSame != 0 || filtered.Summary.NoLinux != 0 || !filtered.Changed {
		t.Fatalf("filtered=%+v", filtered)
	}
	if err := ValidateDiscoveryReport(filtered); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryLocalRejectsOversizedAndMalformedMetadataWithoutNetwork(t *testing.T) {
	for _, test := range []string{"oversize", "malformed"} {
		t.Run(test, func(t *testing.T) {
			opts := discoveryFixture(t, []quiverApp{}, []quiverPlatformEntry{}, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
			path := filepath.Join(strings.TrimPrefix(opts.Catalog, "quiver:"), "index.json")
			if test == "oversize" {
				file, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxCatalogMetadataBytes + 1); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(`{not-json`), 0600); err != nil {
				t.Fatal(err)
			}
			opts.ResolveIdentity = func(context.Context, string, string) (string, error) {
				t.Fatal("lookup on invalid input")
				return "", nil
			}
			if _, err := Discover(context.Background(), opts); err == nil {
				t.Fatal("invalid local metadata accepted")
			}
		})
	}
}

func TestDiscoveryRedirectPolicyRejectsUnsafeLocations(t *testing.T) {
	for _, location := range []string{"http://api.github.com/repos/owner/new", "https://evil.invalid/repos/owner/new", "https://user:password@api.github.com/repos/owner/new"} {
		t.Run(location, func(t *testing.T) {
			opts := discoveryFixture(t, []quiverApp{fixtureApp("a", "github", "owner/old")}, []quiverPlatformEntry{fixturePlatform("github", "owner/old", "v1", "Game.AppImage")}, []discoveryRegistryFixture{{"game", "gitlab", "group/game", "v1"}})
			opts.ResolveIdentity = nil
			calls := 0
			opts.HTTP = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls != 1 {
					t.Fatal("unsafe redirect reached transport")
				}
				response := jsonResponse(http.StatusFound, "")
				response.Header.Set("Location", location)
				return response, nil
			})}
			report, err := Discover(context.Background(), opts)
			if err != nil || len(report.Entries) != 1 || report.Entries[0].Status != "ambiguous" || calls != 1 {
				t.Fatalf("report=%+v err=%v calls=%d", report, err, calls)
			}
		})
	}
}

func TestDiscoveryPreferencePreservesEveryVariantAndAlternativeFilename(t *testing.T) {
	preferred := []string{"Game-arm64.AppImage", "Game-x86_64-EU.AppImage", "Game-x86_64-portable.AppImage", "Game-SteamDeck.AppImage"}
	alternatives := []DiscoveryAssetGroup{
		{Format: "tar.gz", Assets: []string{"Game-linux-amd64.tar.gz", "Game-linux-arm64.tar.gz"}},
		{Format: "tar.xz", Assets: []string{"Game-linux-amd64.tar.xz"}},
		{Format: "zip", Assets: []string{"Game-linux-amd64.zip"}},
	}
	assets := append([]string{}, preferred...)
	for _, group := range alternatives {
		assets = append(assets, group.Assets...)
	}
	assets = append(assets, "game-windows-x64.zip", "game-macos-arm64.zip")
	entry := DiscoveryEntry{Preferred: DiscoveryAssetGroup{Assets: []string{}}, Alternatives: []DiscoveryAssetGroup{}, Architectures: []string{}, LinuxAssets: []string{}}
	classifyAssets(&entry, fixturePlatform("github", "owner/game", "v1", assets...), "")
	finalizeAssetPreference(&entry)
	if entry.Preferred.Format != "appimage" || !sameStrings(entry.Preferred.Assets, preferred) || !reflect.DeepEqual(entry.Alternatives, alternatives) || !reflect.DeepEqual(entry.Architectures, []string{"amd64", "arm64", "unknown"}) {
		t.Fatalf("lost evidence: %+v", entry)
	}
	for _, name := range []string{"Game-amd64.deb", "Game-linux-i386.AppImage"} {
		entry := DiscoveryEntry{CatalogRelease: "v1", Preferred: DiscoveryAssetGroup{Assets: []string{}}, Alternatives: []DiscoveryAssetGroup{}}
		classifyAssets(&entry, fixturePlatform("github", "owner/game", "v1", name), "")
		if classifyDiscoveryStatus(entry) != "unsupported" {
			t.Fatalf("unsupported capability admitted: %+v", entry)
		}
	}
	entry = DiscoveryEntry{CatalogRelease: "v1", Preferred: DiscoveryAssetGroup{Assets: []string{}}, Alternatives: []DiscoveryAssetGroup{}}
	classifyAssets(&entry, fixturePlatform("github", "owner/game", "v1", "Game-1.386.0-linux.zip"), "")
	if !reflect.DeepEqual(entry.Architectures, []string{"unknown"}) {
		t.Fatalf("version number treated as architecture: %+v", entry)
	}
}

func TestDiscoveryReleaseComparisonRejectsMissingMixedMatches(t *testing.T) {
	for _, matches := range [][]DiscoveryRegistryMatch{
		{{ID: "a", Release: "v2"}, {ID: "b", Release: ""}},
		{{ID: "a", Release: ""}, {ID: "b", Release: "v2"}},
	} {
		entry := DiscoveryEntry{CatalogRelease: "v1", LinuxAssets: []string{"Game.AppImage"}, Preferred: DiscoveryAssetGroup{Format: "appimage", Assets: []string{"Game.AppImage"}}, RegistryMatches: matches}
		if classifyDiscoveryStatus(entry) != "ambiguous" {
			t.Fatalf("missing match compared confidently: %+v", entry)
		}
	}
	entry := DiscoveryEntry{CatalogRelease: "v1", LinuxAssets: []string{"Game.AppImage"}, Preferred: DiscoveryAssetGroup{Format: "appimage", Assets: []string{"Game.AppImage"}}, RegistryMatches: []DiscoveryRegistryMatch{{ID: "a", Release: "v1"}, {ID: "b", Release: "v2"}}}
	if classifyDiscoveryStatus(entry) != "release-diff" {
		t.Fatal("mixed exact/different matches lost")
	}
}

func TestDiscoveryTypedContractAndIncompletePairFailures(t *testing.T) {
	report := DiscoveryReport{SchemaVersion: 1, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: "/local"}, Registry: DiscoveryRegistry{Source: "/registry"}, Entries: []DiscoveryEntry{}}
	for _, invalid := range []DiscoveryReport{
		{SchemaVersion: 1, Catalog: report.Catalog, Registry: report.Registry},
		{SchemaVersion: 1, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: "/local", Revision: strings.Repeat("a", 40)}, Registry: report.Registry, Entries: []DiscoveryEntry{}},
		{SchemaVersion: 1, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: "https://github.com/owner/catalog"}, Registry: report.Registry, Entries: []DiscoveryEntry{}},
	} {
		if err := ValidateDiscoveryReport(invalid); err == nil {
			t.Fatalf("invalid contract accepted: %+v", invalid)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "discovery.md"), []byte("preserve-existing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDiscoveryReports(dir, report); err == nil {
		t.Fatal("partial pair accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "discovery.json")); !os.IsNotExist(err) {
		t.Fatal("failed pair published JSON")
	}
	md, err := os.ReadFile(filepath.Join(dir, "discovery.md"))
	if err != nil || string(md) != "preserve-existing" {
		t.Fatal("existing report lost on failure")
	}
}

func TestDiscoveryRejectsSharedRepositoryEvidenceAmplification(t *testing.T) {
	apps := make([]quiverApp, 64)
	for i := range apps {
		apps[i] = fixtureApp(fmt.Sprintf("app-%d", i), "github", "owner/game")
	}
	opts := discoveryFixture(t, apps, []quiverPlatformEntry{fixturePlatform("github", "owner/game", "v1", strings.Repeat("x", 300<<10)+".AppImage")}, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
	_, err := Discover(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "evidence exceeds size limit") {
		t.Fatalf("amplified input accepted: %v", err)
	}
}

func TestDiscoveryFilterDistinguishesNonLinuxFromUnresolvedSelection(t *testing.T) {
	app := fixtureApp("a", "github", "owner/game")
	app.ReleaseAssetFilter = "portable"
	opts := discoveryFixture(t, []quiverApp{app}, []quiverPlatformEntry{fixturePlatform("github", "owner/game", "v1", "Game-windows-portable.zip", "Game-linux.AppImage")}, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
	report, err := Discover(context.Background(), opts)
	if err != nil || report.Entries[0].Status != "no-linux" {
		t.Fatalf("selected non-Linux asset misclassified: %+v %v", report, err)
	}
	app.ReleaseAssetFilter = "nonexistent"
	writeFixtureJSON(t, filepath.Join(strings.TrimPrefix(opts.Catalog, "quiver:"), "community-app-catalog/OtherPlatforms.json"), map[string]any{"name": "OtherPlatforms", "apps": []quiverApp{app}})
	report, err = Discover(context.Background(), opts)
	if err != nil || report.Entries[0].Status != "ambiguous" {
		t.Fatalf("unresolved selection misclassified: %+v %v", report, err)
	}
}

func TestDiscoveryRejectsExcessiveEntryAndAssetCounts(t *testing.T) {
	for _, kind := range []string{"applications", "assets", "empty filename"} {
		t.Run(kind, func(t *testing.T) {
			apps := []quiverApp{fixtureApp("game", "github", "owner/game")}
			assets := []string{"Game.AppImage"}
			switch kind {
			case "applications":
				apps = make([]quiverApp, maxCatalogEntries+1)
				for i := range apps {
					apps[i] = fixtureApp(fmt.Sprintf("app-%d", i), "github", "owner/game")
				}
			case "assets":
				assets = make([]string, maxCatalogAssets+1)
				for i := range assets {
					assets[i] = "Game.AppImage"
				}
			case "empty filename":
				assets = []string{""}
			}
			opts := discoveryFixture(t, apps, []quiverPlatformEntry{fixturePlatform("github", "owner/game", "v1", assets...)}, []discoveryRegistryFixture{{"game", "github", "owner/game", "v1"}})
			if _, err := Discover(context.Background(), opts); err == nil {
				t.Fatal("unbounded or malformed metadata accepted")
			}
		})
	}
}
