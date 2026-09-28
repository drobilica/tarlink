package research

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverLocalQuiverPreferenceAndComparison(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog")
	registry := filepath.Join(root, "registry")
	for _, p := range []string{"community-app-catalog/Nintendo.json", "community-app-catalog/PlayStation.json", "community-app-catalog/Xbox.json", "community-app-catalog/OtherPlatforms.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(catalog, p)), 0755); err != nil {
			t.Fatal(err)
		}
		group := strings.TrimSuffix(filepath.Base(p), ".json")
		if err := os.WriteFile(filepath.Join(catalog, p), []byte(fmt.Sprintf(`{"name":%q,"apps":[]}`, group)), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(catalog, "index.json"), []byte(`{"version":2,"platformMetadataUrl":"https://example/platform-index.json","lists":[{"id":"1"},{"id":"2"},{"id":"3"},{"id":"4"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "platform-index.json"), []byte(`{"formatRevision":1,"entries":[{"provider":"github","repository":"Owner/Game","releaseTag":"v2","assetNames":["Game.AppImage","Game-linux-amd64.tar.gz","Game-win-x86_64.zip"]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalog, "community-app-catalog/OtherPlatforms.json"), []byte(`{"name":"OtherPlatforms","apps":[{"name":"Game","repository":"Owner/Game","folderName":"game","appIconUrl":"https://example/icon"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(registry, "apps/game"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("schema: 5\nid: game\nname: Game\nsummary: Game\nhomepage: https://github.com/owner/game\ncategories: [games]\nrelease:\n  current: v1\n  verification: {algorithm: sha256}\n  releases:\n    - version: v1\n      artifacts:\n        linux-amd64:\n          archive: appimage\n          url: https://github.com/owner/game/releases/download/v1/Game.AppImage\n          verification: {digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789, source: https://github.com/owner/game/releases/assets/1}\napplication:\n  executable:\n    path: appimage\n    create-bin-link: false\ndesktop:\n  categories: [Game]\n")
	if err := os.WriteFile(filepath.Join(registry, "apps/game/manifest.yaml"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Discover(context.Background(), DiscoveryOptions{Catalog: "quiver:" + catalog, Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Entries) != 1 || r.Entries[0].Status != "release-diff" {
		t.Fatalf("report=%+v", r)
	}
	if r.Entries[0].Preferred.Format != "appimage" || len(r.Entries[0].Preferred.Assets) != 1 || len(r.Entries[0].Alternatives) != 1 {
		t.Fatalf("assets=%+v", r.Entries[0])
	}
	if strings.Contains(r.Entries[0].LinuxAssets[0], "win") {
		t.Fatal("Windows asset treated as Linux")
	}
	b1, _ := json.Marshal(r)
	b2, _ := json.Marshal(r)
	if string(b1) != string(b2) {
		t.Fatal("nondeterministic JSON")
	}
}

func TestDiscoverRejectsCatalogSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(root, "outside.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	c := filepath.Join(root, "catalog")
	if err := os.MkdirAll(filepath.Join(c, "community-app-catalog"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(c, "index.json")); err != nil {
		t.Skip("symlinks unavailable")
	}
	_, err := Discover(context.Background(), DiscoveryOptions{Catalog: "quiver:" + c, Registry: root})
	if err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestWriteDiscoveryReportsUsesOneResultAndPairsFiles(t *testing.T) {
	dir := t.TempDir()
	report := DiscoveryReport{
		SchemaVersion: 1,
		Catalog:       DiscoveryCatalog{Adapter: "quiver", Source: "/catalog"},
		Registry:      DiscoveryRegistry{Source: "/registry"},
		Summary:       DiscoverySummary{New: 1},
		Entries:       []DiscoveryEntry{{ExternalID: "demo", Name: "A | game", Provider: "github", CatalogRelease: "v1", Preferred: DiscoveryAssetGroup{Format: "appimage", Assets: []string{"a.AppImage"}}, Alternatives: []DiscoveryAssetGroup{}, Status: "new", Architectures: []string{"unknown"}, LinuxAssets: []string{"a.AppImage"}, RegistryMatches: []DiscoveryRegistryMatch{}}},
	}
	if err := WriteDiscoveryReports(dir, report); err != nil {
		t.Fatal(err)
	}
	jsonBytes, err := os.ReadFile(filepath.Join(dir, "discovery.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded DiscoveryReport
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil || decoded.SchemaVersion != 1 || len(decoded.Entries) != 1 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	markdown, err := os.ReadFile(filepath.Join(dir, "discovery.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(markdown), `A \| game`) || strings.Contains(string(markdown), "revision: `local`") {
		t.Fatalf("markdown=%q", markdown)
	}
}

func TestWriteDiscoveryReportsPreservesExistingPair(t *testing.T) {
	dir := t.TempDir()
	report := DiscoveryReport{SchemaVersion: 1, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: "local"}, Registry: DiscoveryRegistry{Source: "registry"}, Summary: DiscoverySummary{}, Entries: []DiscoveryEntry{}}
	if err := os.WriteFile(filepath.Join(dir, "discovery.json"), []byte("old-json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "discovery.md"), []byte("old-md"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteDiscoveryReports(dir, report); err == nil {
		t.Fatal("existing pair accepted")
	}
	jsonBytes, _ := os.ReadFile(filepath.Join(dir, "discovery.json"))
	mdBytes, _ := os.ReadFile(filepath.Join(dir, "discovery.md"))
	if string(jsonBytes) != "old-json" || string(mdBytes) != "old-md" {
		t.Fatalf("existing pair changed: %q %q", jsonBytes, mdBytes)
	}
}

func TestDiscoveryAssetAndStatusRules(t *testing.T) {
	tests := []struct {
		name, status string
		platform     quiverPlatformEntry
		wantPref     string
		wantAlt      int
		wantArch     []string
	}{
		{name: "appimage preference retains variants", status: "new", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-arm64.AppImage", "x-linux-amd64.AppImage", "x-linux-amd64.tar.gz", "x-linux-amd64.zip", "x-windows-x86_64.zip"}}, wantPref: "appimage", wantAlt: 2, wantArch: []string{"amd64", "arm64"}},
		{name: "archive fallback", status: "new", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-amd64.tar.gz", "x-linux-amd64.zip"}}, wantPref: "tar.gz", wantAlt: 1, wantArch: []string{"amd64"}},
		{name: "deb unsupported", status: "unsupported", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-amd64.deb"}}, wantArch: []string{"amd64"}},
		{name: "generic zip no linux", status: "no-linux", platform: quiverPlatformEntry{AssetNames: []string{"release.zip"}}},
		{name: "generic x86 zip no linux", status: "no-linux", platform: quiverPlatformEntry{AssetNames: []string{"release-x86_64.zip"}}},
		{name: "linux token is bounded", status: "no-linux", platform: quiverPlatformEntry{AssetNames: []string{"notlinux-amd64.zip"}}},
		{name: "bare x86 unsupported", status: "unsupported", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-x86.zip"}}, wantArch: []string{"i386"}},
		{name: "multiple architectures retained", status: "new", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-amd64-arm64.AppImage"}}, wantPref: "appimage", wantArch: []string{"amd64", "arm64"}},
		{name: "armhf unsupported", status: "unsupported", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-armhf.tar.gz"}}, wantArch: []string{"armhf"}},
		{name: "raw linux unsupported", status: "unsupported", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-amd64"}}, wantArch: []string{"amd64"}},
		{name: "riscv64 unsupported", status: "unsupported", platform: quiverPlatformEntry{AssetNames: []string{"x-linux-riscv64.AppImage"}}, wantArch: []string{"riscv64"}},
		{name: "existing linux64 primitive", status: "new", platform: quiverPlatformEntry{AssetNames: []string{"x-linux64.zip"}}, wantPref: "zip", wantArch: []string{"amd64"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := DiscoveryEntry{Preferred: DiscoveryAssetGroup{Assets: []string{}}, Alternatives: []DiscoveryAssetGroup{}, LinuxAssets: []string{}, Architectures: []string{}}
			entry.CatalogRelease = "v1"
			classifyAssets(&entry, test.platform, "")
			finalizeAssetPreference(&entry)
			entry.Status = classifyDiscoveryStatus(entry)
			if entry.Status != test.status || entry.Preferred.Format != test.wantPref || len(entry.Alternatives) != test.wantAlt || !equalStrings(entry.Architectures, test.wantArch) {
				t.Fatalf("entry=%+v", entry)
			}
		})
	}
}

func TestDiscoveryStatusPrecedence(t *testing.T) {
	base := DiscoveryEntry{CatalogRelease: "v1", LinuxAssets: []string{"x-linux-amd64.zip"}, Preferred: DiscoveryAssetGroup{Format: "zip", Assets: []string{"x-linux-amd64.zip"}}, Alternatives: []DiscoveryAssetGroup{}, RegistryMatches: []DiscoveryRegistryMatch{}}
	if got := classifyDiscoveryStatus(base); got != "new" {
		t.Fatalf("new=%s", got)
	}
	base.CatalogRelease = ""
	if got := classifyDiscoveryStatus(base); got != "ambiguous" {
		t.Fatalf("missing catalog release=%s", got)
	}
	base.CatalogRelease = "v1"
	base.RegistryMatches = []DiscoveryRegistryMatch{{ID: "app", Release: ""}}
	if got := classifyDiscoveryStatus(base); got != "ambiguous" {
		t.Fatalf("missing registry release=%s", got)
	}
	base.RegistryMatches = []DiscoveryRegistryMatch{{ID: "app", Release: "v2"}}
	if got := classifyDiscoveryStatus(base); got != "release-diff" {
		t.Fatalf("diff=%s", got)
	}
	base.Reasons = []string{"identity unresolved"}
	if got := classifyDiscoveryStatus(base); got != "ambiguous" {
		t.Fatalf("reason=%s", got)
	}
}

func TestValidateDiscoveryReportRejectsNullArraysAndBadSummary(t *testing.T) {
	report := DiscoveryReport{SchemaVersion: 1, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: "local"}, Registry: DiscoveryRegistry{Source: "registry"}, Summary: DiscoverySummary{New: 1}, Entries: []DiscoveryEntry{{ExternalID: "x", Provider: "github", CatalogRelease: "v1", Status: "new", Architectures: []string{}, LinuxAssets: []string{"x-linux-amd64.zip"}, Preferred: DiscoveryAssetGroup{Format: "zip", Assets: []string{"x-linux-amd64.zip"}}, Alternatives: []DiscoveryAssetGroup{}, RegistryMatches: []DiscoveryRegistryMatch{}}}}
	if err := ValidateDiscoveryReport(report); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	report.Entries[0].Architectures = nil
	if err := ValidateDiscoveryReport(report); err == nil {
		t.Fatal("null array accepted")
	}
	report.Entries[0].Architectures = []string{}
	report.Summary.New = 0
	if err := ValidateDiscoveryReport(report); err == nil {
		t.Fatal("bad summary accepted")
	}
}

func TestNormalizeRepositoryKeepsProviderAndNamespaceBoundaries(t *testing.T) {
	tests := []struct {
		provider, raw string
		want          string
		ok            bool
	}{
		{"github", "https://github.com/Owner/Repo.git", "owner/repo", true},
		{"gitlab", "https://gitlab.com/Group/Sub/Repo", "group/sub/repo", true},
		{"github", "https://gitlab.com/owner/repo", "", false},
		{"github", "https://github.com/owner/repo/evil", "", false},
		{"github", "https://github.com/owner/%2Frepo", "", false},
		{"github", "https://user:pass@github.com/owner/repo", "", false},
		{"github", "https://evil.example/owner/repo", "", false},
	}
	for _, test := range tests {
		got, err := normalizeRepository(test.provider, test.raw)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("normalizeRepository(%q, %q) = %q, %v", test.provider, test.raw, got, err)
		}
	}
}

func TestCatalogSourcesRejectDotSegmentsAndUnsafeMetadataNames(t *testing.T) {
	for _, source := range []string{
		"https://github.com/./repo",
		"https://github.com/owner/.",
		"https://github.com/owner/..",
		"https://github.com/owner/repo?ref=main",
	} {
		if _, _, err := githubSource(source); err == nil {
			t.Errorf("unsafe source accepted: %q", source)
		}
	}
	for _, name := range []string{"index.json?x", "index\\.json", "index\x00.json", "../index.json"} {
		if _, err := safeCatalogPath(name); err == nil {
			t.Errorf("unsafe metadata path accepted: %q", name)
		}
	}
}

func TestRemoteReaderPinsRevisionAndGitLabDoesNotReceiveGitHubToken(t *testing.T) {
	var requests []struct {
		url  string
		auth string
	}
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, struct {
			url  string
			auth string
		}{req.URL.String(), req.Header.Get("Authorization")})
		if req.URL.Host == "api.github.com" && strings.HasPrefix(req.URL.Path, "/repos/o/r/contents/") {
			body := base64.StdEncoding.EncodeToString([]byte(`{"ok":true}`))
			return jsonResponse(http.StatusOK, fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`, body)), nil
		}
		if req.URL.Host == "gitlab.com" {
			return jsonResponse(http.StatusOK, `{"path_with_namespace":"group/project"}`), nil
		}
		return jsonResponse(http.StatusNotFound, `{}`), nil
	})
	client := &Client{HTTP: &http.Client{Transport: transport}}
	reader := remoteCatalogReader{client: client, owner: "o", repo: "r", revision: strings.Repeat("a", 40)}
	if _, err := reader.Read(context.Background(), "index.json"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests=%v", requests)
	}
	parsed, err := url.Parse(requests[0].url)
	if err != nil || parsed.Query().Get("ref") != strings.Repeat("a", 40) {
		t.Fatalf("unpinned request %q", requests[0].url)
	}
	t.Setenv("GH_TOKEN", "secret")
	if got, err := remoteIdentityResolver(client)(context.Background(), "gitlab", "group/project"); err != nil || got != "group/project" {
		t.Fatalf("GitLab resolver = %q, %v", got, err)
	}
	if len(requests) != 2 || requests[1].auth != "" {
		t.Fatalf("GitLab request leaked token: %+v", requests)
	}
}

func TestRemoteReaderRejectsUnsafePathsAndPreferredReleaseShape(t *testing.T) {
	reader := remoteCatalogReader{client: &Client{HTTP: &http.Client{}}, owner: "o", repo: "r", revision: strings.Repeat("a", 40)}
	if _, err := reader.Read(context.Background(), "../index.json"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if tag, known := preferredReleaseTag(json.RawMessage(`{"tag":"v1"}`)); known || tag != "" {
		t.Fatalf("unknown preferred release interpreted: %q, %v", tag, known)
	}
	if tag, known := preferredReleaseTag(json.RawMessage("null")); !known || tag != "" {
		t.Fatalf("null preferred release = %q, %v", tag, known)
	}
}

func TestRemoteReaderRejectsHTTPAndOversizedMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		code int
	}{
		{"http error", `{}`, http.StatusForbidden},
		{"malformed JSON", `{not-json`, http.StatusOK},
		{"oversized response", strings.Repeat("x", int(maxCatalogMetadataBytes)+1), http.StatusOK},
		{"oversized decoded file", fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("x"), int(maxCatalogMetadataBytes)+1))), http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &Client{HTTP: &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return jsonResponse(test.code, test.body), nil
			})}}
			reader := remoteCatalogReader{client: client, owner: "o", repo: "r", revision: strings.Repeat("a", 40)}
			if _, err := reader.Read(context.Background(), "index.json"); err == nil {
				t.Fatal("invalid remote metadata accepted")
			}
		})
	}
}

func TestDiscoverRemotePinsDefaultBranchCommitAndReadsAllMetadataAtSHA(t *testing.T) {
	root := t.TempDir()
	registryRoot := filepath.Join(root, "registry")
	if err := os.MkdirAll(filepath.Join(registryRoot, "apps", "game"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte("schema: 5\nid: game\nname: Game\nsummary: Game\nhomepage: https://github.com/owner/game\ncategories: [games]\nrelease:\n  current: v1\n  verification: {algorithm: sha256}\n  releases:\n    - version: v1\n      artifacts:\n        linux-amd64:\n          archive: appimage\n          url: https://github.com/owner/game/releases/download/v1/Game.AppImage\n          verification: {digest: abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789, source: https://github.com/owner/game/releases/assets/1}\napplication:\n  executable:\n    path: appimage\n    create-bin-link: false\ndesktop:\n  categories: [Game]\n")
	if err := os.WriteFile(filepath.Join(registryRoot, "apps", "game", "manifest.yaml"), manifest, 0644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"index.json":                                `{ "version": 2, "platformMetadataUrl": "https://example/platform-index.json", "lists": [{"id":"1"},{"id":"2"},{"id":"3"},{"id":"4"}] }`,
		"platform-index.json":                       `{ "formatRevision": 1, "entries": [{"provider":"github","repository":"owner/game","releaseTag":"v1","assetNames":["Game-linux-amd64.AppImage"]}] }`,
		"community-app-catalog/Nintendo.json":       `{ "name":"Nintendo", "apps":[] }`,
		"community-app-catalog/PlayStation.json":    `{ "name":"PlayStation", "apps":[] }`,
		"community-app-catalog/Xbox.json":           `{ "name":"Xbox", "apps":[] }`,
		"community-app-catalog/OtherPlatforms.json": `{ "name":"Other", "apps":[{"name":"Game","repository":"owner/game","folderName":"game"}] }`,
	}
	sha := strings.Repeat("b", 40)
	var requests []string
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		requests = append(requests, req.URL.String())
		switch {
		case req.URL.Path == "/repos/owner/catalog":
			return jsonResponse(http.StatusOK, `{"default_branch":"catalog/stable"}`), nil
		case req.URL.Path == "/repos/owner/catalog/git/ref/heads/catalog/stable":
			return jsonResponse(http.StatusOK, fmt.Sprintf(`{"object":{"sha":%q,"type":"commit"}}`, sha)), nil
		case strings.HasPrefix(req.URL.Path, "/repos/owner/catalog/contents/"):
			name := strings.TrimPrefix(req.URL.Path, "/repos/owner/catalog/contents/")
			body, ok := files[name]
			if !ok {
				return jsonResponse(http.StatusNotFound, `{}`), nil
			}
			encoded := base64.StdEncoding.EncodeToString([]byte(body))
			return jsonResponse(http.StatusOK, fmt.Sprintf(`{"type":"file","encoding":"base64","content":%q}`, encoded)), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})
	report, err := Discover(context.Background(), DiscoveryOptions{Catalog: "quiver:https://github.com/owner/catalog", Registry: registryRoot, HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Catalog.Revision != sha || len(report.Entries) != 1 || report.Entries[0].Status != "registered-same" {
		t.Fatalf("report=%+v", report)
	}
	if len(requests) != 8 {
		t.Fatalf("expected repository/ref and six metadata requests only: %v", requests)
	}
	for _, request := range requests {
		if strings.Contains(request, "contents/") && !strings.Contains(request, "ref="+sha) {
			t.Fatalf("metadata request was not pinned: %s", request)
		}
	}
}

func TestDecodeQuiverRejectsWrongShapedCatalogFiles(t *testing.T) {
	files := []string{"index.json", "platform-index.json", "a.json", "b.json", "c.json", "d.json"}
	data := map[string][]byte{
		"index.json":          []byte(`{"version":2,"platformMetadataUrl":"https://example/platform-index.json","lists":[{"id":"1"},{"id":"2"},{"id":"3"},{"id":"4"}]}`),
		"platform-index.json": []byte(`{"formatRevision":1,"entries":[]}`),
	}
	for _, name := range files[2:] {
		data[name] = []byte(`{"name":"group","apps":null}`)
	}
	if _, _, _, err := decodeQuiverMetadata(data, files); err == nil {
		t.Fatal("null apps array accepted")
	}
	data["platform-index.json"] = []byte(`{"formatRevision":1}`)
	for _, name := range files[2:] {
		data[name] = []byte(`{"name":"group","apps":[]}`)
	}
	if _, _, _, err := decodeQuiverMetadata(data, files); err == nil {
		t.Fatal("missing platform entries accepted")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d", status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
