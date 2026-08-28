package research

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
	"fmt"
	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type cancelBody struct{}

func (cancelBody) Read([]byte) (int, error) { return 0, context.Canceled }
func (cancelBody) Close() error             { return nil }

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func testDir(t *testing.T) string {
	base := ""
	if _, err := os.Stat("/private/tmp"); err == nil {
		base = "/private/tmp"
	}
	d, err := os.MkdirTemp(base, "tarlink-research-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(d) })
	return d
}

func TestParseRepositoryAndIdentity(t *testing.T) {
	for _, in := range []string{"OWNER/REPO", "https://github.com/OWNER/REPO"} {
		got, e := ParseRepository(in)
		if e != nil || got != "owner/repo" {
			t.Fatalf("%q => %q %v", in, got, e)
		}
	}
	for _, in := range []string{"http://github.com/O/R", "github.com/O/R", "https://github.com/O/R/issues", "O/R/x", "api.github.com/O/R"} {
		if _, e := ParseRepository(in); e == nil {
			t.Errorf("accepted %q", in)
		}
	}
	r := Release{ID: 10, Repository: "o/r"}
	a := Asset{ID: 100, ReleaseID: 10, Repository: "o/r", State: "uploaded", Digest: "sha256:" + strings.Repeat("a", 64)}
	if EvaluateProvenance("o/r", r, a).Verdict != Acceptable {
		t.Fatal("expected acceptable")
	}
	a.ID = 200
	if EvaluateProvenance("O/R", r, a).ReasonCode != "" { /* identity is still structurally associated; ID itself is immutable */
	}
}

func TestParseRepositoryRejectsHostileForms(t *testing.T) {
	for _, in := range []string{"O/../R", "O/./R", "O//R", "/O/R", "O/R/", "O/R/x", "https://github.com/O/R/", "https://github.com/O/R?x=1", "https://github.com/O/R%2Fx", "https://github.com.evil/O/R", "https://github.com:443/O/R", "https://user@github.com/O/R"} {
		if got, err := ParseRepository(in); err == nil {
			t.Errorf("accepted hostile repository %q as %q", in, got)
		}
	}
}

func TestParseReleaseAssetURL(t *testing.T) {
	target, err := ParseReleaseAssetURL("https://github.com/OWNER/REPO/releases/download/v1%2Fbeta/app-linux-x64.zip")
	if err != nil {
		t.Fatal(err)
	}
	if target.Repository != "owner/repo" || target.Tag != "v1/beta" || target.Asset != "app-linux-x64.zip" {
		t.Fatalf("target=%#v", target)
	}
	for _, raw := range []string{
		"http://github.com/o/r/releases/download/v1/a.zip",
		"https://github.com.evil/o/r/releases/download/v1/a.zip",
		"https://github.com/o/r/releases/download/v1/a.zip?x=1",
		"https://github.com/o/r/releases/download/v1/a.zip/",
		"https://github.com/o/r/releases/tag/v1/a.zip",
		"https://github.com/o/r/releases/download/v1/../a.zip",
	} {
		if _, err := ParseReleaseAssetURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
}

func TestInferPlatformConservative(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"app-linux-x64.zip", "linux-amd64"},
		{"app-linux-amd64.tar.gz", "linux-amd64"},
		{"app-linux-x86_64.tar.xz", "linux-amd64"},
		{"app-linux64.zip", "linux-amd64"},
		{"app-linux-aarch64.tar.gz", "linux-arm64"},
		{"app-linux-arm64.tar.gz", "linux-arm64"},
		{"app-x64.zip", ""},
		{"app-arm64.zip", ""},
		{"app-windows-x64.zip", ""},
	} {
		if got := InferPlatform(tc.name); got != tc.want {
			t.Errorf("InferPlatform(%q)=%q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestTargetArchitectureFromAssetName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"app-linux-x64.zip", "amd64"},
		{"app-linux-amd64.tar.gz", "amd64"},
		{"app-linux64.zip", "amd64"},
		{"app-linux-aarch64.tar.gz", "arm64"},
		{"app-linux-arm64.tar.gz", "arm64"},
		{"app-x64.zip", ""},
		{"app-arm64.zip", ""},
		{"app-linux-x64.AppImage", "amd64"},
	} {
		if got := TargetArchitecture(tc.name); got != tc.want {
			t.Errorf("TargetArchitecture(%q)=%q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestResolveReleaseAssetExactIdentity(t *testing.T) {
	body := `{"id":20,"tag_name":"v1","assets":[{"id":30,"name":"app.zip","browser_download_url":"https://objects.example/app.zip","size":4,"state":"uploaded"}]}`
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/o/r/releases/tags/v1" {
			t.Fatalf("request path=%s", r.URL.Path)
		}
		return response(200, body), nil
	})}}
	target := ReleaseAssetTarget{Repository: "o/r", Tag: "v1", Asset: "app.zip"}
	resolved, err := c.ResolveReleaseAsset(context.Background(), target)
	if err != nil || resolved.Repository != "o/r" || resolved.Release.ID != 20 || resolved.Asset.ID != 30 {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	_, err = c.ResolveReleaseAsset(context.Background(), ReleaseAssetTarget{Repository: "o/r", Tag: "v1", Asset: "missing.zip"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != APIErrorNotFound {
		t.Fatalf("missing err=%v", err)
	}
}

func TestDiscoverRepositoryIconCandidatesUsesCompleteRecursiveTree(t *testing.T) {
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	blob := strings.Repeat("c", 40)
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/o/r/git/ref/tags/v1":
			return response(200, `{"ref":"refs/tags/v1","object":{"sha":"`+commit+`","type":"commit"}}`), nil
		case "/repos/o/r/git/commits/" + commit:
			return response(200, `{"sha":"`+commit+`","tree":{"sha":"`+tree+`"}}`), nil
		case "/repos/o/r/git/trees/" + tree:
			if r.URL.RawQuery != "recursive=1" {
				t.Fatalf("tree query = %q", r.URL.RawQuery)
			}
			return response(200, `{"sha":"`+tree+`","truncated":false,"tree":[{"path":"icons/512.png","type":"blob","sha":"`+blob+`","size":4},{"path":"icons/512.svg","type":"blob","sha":"`+blob+`","size":4},{"path":"README.md","type":"blob","sha":"`+blob+`","size":4}]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}}
	files, err := c.DiscoverRepositoryIconCandidates(context.Background(), "O/R", "v1")
	if err != nil || len(files) != 1 || files[0].Path != "icons/512.png" {
		t.Fatalf("files=%#v err=%v", files, err)
	}
	if files[0].URL != "https://raw.githubusercontent.com/o/r/"+commit+"/icons/512.png" {
		t.Fatalf("URL=%q", files[0].URL)
	}
}

func TestDiscoverRepositoryIconCandidatesRejectsTruncatedTree(t *testing.T) {
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/o/r/git/ref/tags/v1":
			return response(200, `{"ref":"refs/tags/v1","object":{"sha":"`+commit+`","type":"commit"}}`), nil
		case "/repos/o/r/git/commits/" + commit:
			return response(200, `{"sha":"`+commit+`","tree":{"sha":"`+tree+`"}}`), nil
		case "/repos/o/r/git/trees/" + tree:
			return response(200, `{"sha":"`+tree+`","truncated":true,"tree":[]}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL)
			return nil, nil
		}
	})}}
	_, err := c.DiscoverRepositoryIconCandidates(context.Background(), "o/r", "v1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind != APIErrorMalformed {
		t.Fatalf("err=%v", err)
	}
}

func TestProvenanceRejectsIncompleteAssetState(t *testing.T) {
	a := Asset{ID: 2, ReleaseID: 1, Repository: "o/r", Digest: "sha256:" + strings.Repeat("a", 64), State: "created"}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)
	if p.ReasonCode != "ASSET_NOT_AVAILABLE" || p.Verdict != Rejected {
		t.Fatalf("got %#v", p)
	}
}

func TestDiscoverReleaseUsesExactTagIncludingSlash(t *testing.T) {
	var got string
	body := `{"id":20,"tag_name":"v1/beta","url":"https://api.github.com/ignored","assets":[{"id":30,"name":"x.zip","browser_download_url":"https://objects.example/x","size":1,"state":"uploaded","uploader":{"id":1}}]}`
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { got = r.URL.Path; return response(200, body), nil })}}
	r, err := c.DiscoverRelease(context.Background(), "O/R", "v1/beta")
	if err != nil || r.ID != 20 || !strings.Contains(got, "releases/tags/") {
		t.Fatalf("release=%#v path=%s err=%v", r, got, err)
	}
}

func TestEvaluateProvenanceRejectsReleaseRepositoryMismatch(t *testing.T) {
	a := Asset{ID: 2, ReleaseID: 1, Repository: "o/r", State: "uploaded", Digest: "sha256:" + strings.Repeat("a", 64)}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "other/repo"}, a)
	if p.ReasonCode != "ASSET_IDENTITY_CHANGED" {
		t.Fatalf("%#v", p)
	}
}

func TestAPI403ClassificationAndCancellation(t *testing.T) {
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		x := response(403, "")
		x.Header.Set("X-RateLimit-Remaining", "1")
		return x, nil
	})}}
	_, err := c.Discover(context.Background(), "O/R")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Kind != APIErrorAuth {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	_, err = c.Discover(ctx, "O/R")
	if !errors.As(err, &ae) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestInspectRejectsCacheSymlink(t *testing.T) {
	d := testDir(t)
	target := filepath.Join(d, "target")
	path := filepath.Join(d, "artifact")
	if err := os.WriteFile(target, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), Artifact{Path: path, Provenance: Provenance{Verdict: Acceptable, Algorithm: "sha256", Digest: strings.Repeat("a", 64)}}, "", ""); !errors.Is(err, ErrCacheCorrupt) {
		t.Fatalf("err=%v", err)
	}
}

func TestProvenanceDigestContractTable(t *testing.T) {
	base := Asset{ID: 2, ReleaseID: 1, Repository: "o/r", State: "uploaded"}
	rel := Release{ID: 1, Repository: "o/r"}
	for _, tc := range []struct{ name, digest, reason string }{
		{"sha512", "sha512:" + strings.Repeat("a", 128), ""},
		{"uppercase-algorithm", "SHA256:" + strings.Repeat("a", 64), "UNSUPPORTED_DIGEST_ALGORITHM"},
		{"wrong-length", "sha256:" + strings.Repeat("a", 63), "MALFORMED_DIGEST"},
		{"malformed", "sha256", "MALFORMED_DIGEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := base
			a.Digest = tc.digest
			p := EvaluateProvenance("o/r", rel, a)
			if tc.reason == "" && p.Verdict != Acceptable {
				t.Fatalf("%#v", p)
			}
			if tc.reason != "" && p.ReasonCode != tc.reason {
				t.Fatalf("%#v", p)
			}
		})
	}
}

func TestForgedProvenanceAndIdentityMismatchRejected(t *testing.T) {
	a := Asset{ID: 2, ReleaseID: 1, Repository: "o/r", State: "uploaded", Size: 1, Digest: "sha256:" + strings.Repeat("a", 64)}
	c := &Client{CacheRoot: testDir(t)}
	for _, p := range []Provenance{{Verdict: Acceptable, Algorithm: "sha256", Digest: strings.Repeat("a", 64)}, {Verdict: Acceptable, Repository: "o/r", ReleaseID: 1, AssetID: 2, AssetState: "uploaded", AssetSize: 1, Algorithm: "sha256", Digest: strings.Repeat("a", 64)}} {
		if _, err := c.Fetch(context.Background(), a, p); err == nil {
			t.Fatal("forged provenance accepted")
		}
	}
}

func TestDiscoveryCacheHostileMetadataRefreshes(t *testing.T) {
	d := testDir(t)
	now := time.Unix(1000, 0)
	body := `[{"id":1,"tag_name":"v","assets":[]}]`
	calls := 0
	c := &Client{CacheRoot: d, APIBase: "https://api.example", Now: func() time.Time { return now }, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, body), nil })}}
	if _, err := c.Discover(context.Background(), "O/R"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "discovery", cacheName("o/r")+".json")
	for _, raw := range []string{`{"version":99}`, `{"version":1,"fetched_at":"1970-01-01T00:16:40Z","releases":[]} trailing`, `{"version":1,"fetched_at":"2999-01-01T00:00:00Z","releases":[]}`} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, body), nil })}
		if _, err := c.Discover(context.Background(), "O/R"); err != nil {
			t.Fatal(err)
		}
	}
	if calls < 4 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestDiscoveryRefreshSeparatesReleaseAndAssetIdentity(t *testing.T) {
	d := testDir(t)
	calls := 0
	first := `[{"id":10,"tag_name":"v1","assets":[{"id":100,"name":"Linux.zip","browser_download_url":"https://objects.example/a","size":1,"state":"uploaded"}]}]`
	second := `[{"id":20,"tag_name":"v1","assets":[{"id":200,"name":"Linux.zip","browser_download_url":"https://objects.example/b","size":1,"state":"uploaded"}]}]`
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, first), nil
		}
		return response(200, second), nil
	})}}
	a, err := c.Discover(context.Background(), "O/R")
	if err != nil || a[0].ID != 10 || a[0].Assets[0].ID != 100 {
		t.Fatalf("first=%#v err=%v", a, err)
	}
	c.Refresh = true
	b, err := c.Discover(context.Background(), "O/R")
	if err != nil || b[0].ID != 20 || b[0].Assets[0].ID != 200 {
		t.Fatalf("refresh=%#v err=%v", b, err)
	}
}

func TestDiscoverySameIDChangedDigestIsReevaluated(t *testing.T) {
	d := testDir(t)
	body := func(d string) string {
		return `[{"id":10,"tag_name":"v1","assets":[{"id":100,"name":"x","browser_download_url":"https://objects.example/x","size":1,"state":"uploaded","digest":"` + d + `"}]}]`
	}
	calls := 0
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, body("sha256:"+strings.Repeat("a", 64))), nil
		}
		return response(200, body("sha256:"+strings.Repeat("b", 64))), nil
	})}}
	first, err := c.Discover(context.Background(), "O/R")
	if err != nil {
		t.Fatal(err)
	}
	c.Refresh = true
	second, err := c.Discover(context.Background(), "O/R")
	if err != nil {
		t.Fatal(err)
	}
	p1 := EvaluateProvenance("o/r", first[0], first[0].Assets[0])
	p2 := EvaluateProvenance("o/r", second[0], second[0].Assets[0])
	if p1.Digest == p2.Digest {
		t.Fatal("digest conclusion reused")
	}
}

func TestDiscoveryRejectsReleaseRepositoryMismatchAndDuplicates(t *testing.T) {
	valid := func(repo Repository, releases []Release) bool {
		return validDiscovery(repo, discoveryFile{Version: 1, FetchedAt: time.Unix(1, 0), Releases: releases})
	}
	r := Release{ID: 1, Repository: "other/repo", Tag: "v"}
	if valid("o/r", []Release{r}) {
		t.Fatal("repository mismatch accepted")
	}
	r.Repository = "o/r"
	if valid("o/r", []Release{r, r}) {
		t.Fatal("duplicate release accepted")
	}
}

func TestAPICrossOriginRedirectRejected(t *testing.T) {
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		x := response(302, "")
		x.Header.Set("Location", "https://evil.example/releases")
		return x, nil
	})}}
	_, err := c.Discover(context.Background(), "O/R")
	var ae *APIError
	if !errors.As(err, &ae) || ae.Kind != APIErrorRedirect {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoveryCacheSymlinkAndOversizeRefresh(t *testing.T) {
	d := testDir(t)
	path := filepath.Join(d, "discovery", cacheName("o/r")+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, `[]`), nil })}}
	if _, err := c.Discover(context.Background(), "O/R"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("symlink cache reused")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", int(maxResponseBytes+1))), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Discover(context.Background(), "O/R"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("oversize cache reused")
	}
}

func TestCorruptRedownloadNeverReachesParser(t *testing.T) {
	d := testDir(t)
	good := []byte("good")
	sum := sha256.Sum256(good)
	a := Asset{ID: 9, ReleaseID: 1, Repository: "o/r", State: "uploaded", Size: int64(len(good)), URL: "https://objects.example/x", Digest: "sha256:" + hex.EncodeToString(sum[:])}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, "bad"), nil })}}
	parsed := 0
	inspectParserHook = func() error { parsed++; return nil }
	defer func() { inspectParserHook = nil }()
	if _, err := c.InspectAsset(context.Background(), a, p, "", ""); err == nil {
		t.Fatal("bad redownload accepted")
	}
	if parsed != 0 {
		t.Fatal("parser reached")
	}
}

func TestExactTagCacheHitAndRefresh(t *testing.T) {
	d := testDir(t)
	calls := 0
	body := `{"id":7,"tag_name":"v1","assets":[]}`
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, body), nil })}}
	if _, err := c.DiscoverRelease(context.Background(), "O/R", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DiscoverRelease(context.Background(), "O/R", "v1"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("cache calls=%d", calls)
	}
	c.Refresh = true
	if _, err := c.DiscoverRelease(context.Background(), "O/R", "v1"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("refresh calls=%d", calls)
	}
}

func TestArchivePathSwapCannotBypassVerifiedFD(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("app")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	d := testDir(t)
	path := filepath.Join(d, "artifact")
	data := buf.Bytes()
	sum := sha256.Sum256(data)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := Provenance{Verdict: Acceptable, Repository: "o/r", ReleaseID: 1, AssetID: 1, AssetState: "uploaded", AssetSize: int64(len(data)), Algorithm: "sha256", Digest: hex.EncodeToString(sum[:])}
	a := Artifact{Path: path, Size: int64(len(data)), Provenance: p}
	inspectParserHook = func() error { _ = os.Remove(path); _ = os.Symlink("/dev/null", path); return nil }
	defer func() { inspectParserHook = nil }()
	if _, err := Inspect(context.Background(), a, "zip", ""); !errors.Is(err, ErrCacheCorrupt) {
		t.Fatalf("pathname swap bypassed: %v", err)
	}
}

func TestAppImageCopyCorruptionIsRejectedBeforeValidation(t *testing.T) {
	d := testDir(t)
	path := filepath.Join(d, "artifact")
	data := []byte("not-an-appimage")
	sum := sha256.Sum256(data)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	p := Provenance{Verdict: Acceptable, Repository: "o/r", ReleaseID: 1, AssetID: 1, AssetState: "uploaded", AssetSize: int64(len(data)), Algorithm: "sha256", Digest: hex.EncodeToString(sum[:])}
	a := Artifact{Path: path, Size: int64(len(data)), Provenance: p}
	inspectParserHook = func() error {
		if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		return nil
	}
	defer func() { inspectParserHook = nil }()
	if _, err := Inspect(context.Background(), a, "appimage", "amd64"); !errors.Is(err, ErrCacheCorrupt) {
		t.Fatalf("copy source mutation accepted: %v", err)
	}
}

func TestAssetReplacementDoesNotReuseVerifiedCache(t *testing.T) {
	d := testDir(t)
	old, neu := []byte("old"), []byte("new")
	so, sn := sha256.Sum256(old), sha256.Sum256(neu)
	calls := 0
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, string(old)), nil
		}
		return response(200, string(neu)), nil
	})}}
	a := Asset{ID: 100, ReleaseID: 10, Repository: "o/r", State: "uploaded", Name: "Linux.zip", URL: "https://objects.example/x", Size: 3, Digest: "sha256:" + hex.EncodeToString(so[:])}
	p := EvaluateProvenance("o/r", Release{ID: 10, Repository: "o/r"}, a)
	x, e := c.Fetch(context.Background(), a, p)
	if e != nil {
		t.Fatal(e)
	}
	b := a
	b.ID = 200
	b.ReleaseID = 20
	b.Digest = "sha256:" + hex.EncodeToString(sn[:])
	q := EvaluateProvenance("o/r", Release{ID: 20, Repository: "o/r"}, b)
	y, e := c.Fetch(context.Background(), b, q)
	if e != nil {
		t.Fatal(e)
	}
	if x.Path == y.Path {
		t.Fatal("replacement reused old cache")
	}
}

func TestSameIDChangedDigestCannotReuseCache(t *testing.T) {
	d := testDir(t)
	data := []byte("old")
	sum := sha256.Sum256(data)
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, string(data)), nil })}}
	a := Asset{ID: 1, ReleaseID: 1, Repository: "o/r", State: "uploaded", URL: "https://objects.example/x", Size: 3, Digest: "sha256:" + hex.EncodeToString(sum[:])}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)
	if _, e := c.Fetch(context.Background(), a, p); e != nil {
		t.Fatal(e)
	}
	a.Digest = "sha256:" + strings.Repeat("b", 64)
	if _, e := c.Fetch(context.Background(), a, EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)); e == nil {
		t.Fatal("changed digest reused")
	}
}

func TestNoDigestNeverCreatesPersistentArtifact(t *testing.T) {
	d := testDir(t)
	c := &Client{CacheRoot: d, TempRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, "raw"), nil })}}
	a := Asset{ID: 1, ReleaseID: 1, Repository: "o/r", State: "uploaded", URL: "https://objects.example/x", Size: 3}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)
	if p.Verdict != Rejected {
		t.Fatal("missing digest accepted")
	}
	if _, e := c.Fetch(context.Background(), a, p); e == nil {
		t.Fatal("unverified persisted")
	}
	x, e := c.FetchUnverified(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	_ = x.Cleanup()
	if _, e := os.Stat(filepath.Join(d, "artifacts")); !os.IsNotExist(e) {
		t.Fatal("persistent artifact cache created")
	}
}

func TestAPIErrorTaxonomyTable(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   string
	}{{404, APIErrorNotFound}, {401, APIErrorAuth}, {403, APIErrorAuth}, {429, APIErrorRateLimited}, {500, APIErrorServer}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
				x := response(tc.status, "")
				if tc.status == 403 {
					x.Header.Set("X-RateLimit-Remaining", "1")
				}
				return x, nil
			})}}
			_, e := c.Discover(context.Background(), "O/R")
			var ae *APIError
			if !errors.As(e, &ae) || ae.Kind != tc.kind {
				t.Fatalf("%v", e)
			}
		})
	}
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "OK", Body: io.NopCloser(strings.NewReader("[] trailing")), Header: make(http.Header)}, nil
	})}}
	if _, e := c.Discover(context.Background(), "O/R"); e == nil {
		t.Fatal("trailing accepted")
	}
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Status: "OK", Body: cancelBody{}, Header: make(http.Header)}, nil
	})}
	_, e := c.Discover(context.Background(), "O/R")
	if !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel=%v", e)
	}
}

func TestInspectReportsExecutablesAndNestedEvidence(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "game", Method: zip.Store}
	h.SetMode(0755)
	w, _ := z.CreateHeader(h)
	_, _ = w.Write([]byte{0x7f, 'E', 'L', 'F', 2})
	w2, _ := z.Create("inner.tar.gz")
	_, _ = w2.Write([]byte{0x1f, 0x8b, 0x08})
	_ = z.Close()
	d := testDir(t)
	p := filepath.Join(d, "a.zip")
	probe := filepath.Join(d, "probe")
	_ = os.Mkdir(probe, 0700)
	if ee := archive.Extract(context.Background(), bytes.NewReader(buf.Bytes()), probe, archive.FormatZip, archive.DefaultLimits()); ee != nil {
		t.Logf("extract=%v", ee)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	r, e := Inspect(context.Background(), Artifact{Path: p, Size: int64(buf.Len())}, archive.FormatZip, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range r.Blockers {
		if b == "NESTED_ARCHIVE_UNSUPPORTED" {
			t.Fatal("nested archive evidence must not be an unsupported blocker")
		}
	}
	if len(r.Executables) != 1 || r.Executables[0] != "game" || len(r.Nested) != 1 || len(r.Blockers) != 0 {
		t.Fatalf("%#v", r)
	}
}

func TestInspectFindsRankedArchiveIconsWithoutRegularFileNoise(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	readme, _ := z.Create("README.txt")
	_, _ = readme.Write([]byte("not an executable"))
	icon, _ := z.Create("resources/icons/icon.png")
	_, _ = icon.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	logo, _ := z.Create("logo.svg")
	_, _ = logo.Write([]byte("<svg/>"))
	_ = z.Close()
	d := testDir(t)
	p := filepath.Join(d, "icons.zip")
	if err := os.WriteFile(p, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Inspect(context.Background(), Artifact{Path: p, Size: int64(buf.Len())}, archive.FormatZip, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Executables) != 0 || len(r.Icons) != 1 || r.Icons[0] != "resources/icons/icon.png" {
		t.Fatalf("inspection=%#v", r)
	}
}

func TestInspectRetainsEquallyStrongArchiveIconCandidates(t *testing.T) {
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for _, name := range []string{"icons/icon.png", "assets/icons/logo.png"} {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "icons.zip")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Inspect(context.Background(), Artifact{Path: path, Size: int64(buffer.Len())}, archive.FormatZip, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Icons, []string{"assets/icons/logo.png", "icons/icon.png"}) {
		t.Fatalf("icons=%#v", result.Icons)
	}
}

func TestInspectArchiveCancellationIsError(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("x")
	_, _ = w.Write([]byte("x"))
	_ = z.Close()
	d := testDir(t)
	p := filepath.Join(d, "a.zip")
	_ = os.WriteFile(p, buf.Bytes(), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Inspect(ctx, Artifact{Path: p, Size: int64(buf.Len())}, archive.FormatZip, ""); e == nil || errors.Is(e, context.Canceled) == false {
		t.Fatalf("err=%v", e)
	}
}

func TestInspectAppImageArchitectureSemantics(t *testing.T) {
	arm64 := make([]byte, 64)
	copy(arm64[:4], []byte{0x7f, 'E', 'L', 'F'})
	arm64[4] = 2
	arm64[5] = 1
	arm64[6] = 1
	copy(arm64[8:11], []byte{'A', 'I', 2})
	arm64[16] = 2
	arm64[18] = 0xb7
	unsupported := make([]byte, 64)
	copy(unsupported, arm64)
	unsupported[18] = 0x03
	write := func(t *testing.T, data []byte) string {
		t.Helper()
		p := filepath.Join(testDir(t), "x.AppImage")
		if err := os.WriteFile(p, data, 0755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inspect := func(t *testing.T, path, expected string) Inspection {
		t.Helper()
		r, e := Inspect(context.Background(), Artifact{Path: path, Size: 64}, "appimage", expected)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	blocked := func(r Inspection) bool {
		for _, x := range r.Blockers {
			if x == "UNSUPPORTED_ARCH" {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		name        string
		data        []byte
		expected    string
		wantBlocked bool
	}{
		{"arm64-elf-expected-amd64-name-contradiction", arm64, "amd64", true},
		{"arm64-elf-expected-arm64", arm64, "arm64", false},
		{"arm64-elf-ambiguous-name", arm64, "", false},
		{"unsupported-elf-machine-ambiguous-name", unsupported, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := inspect(t, write(t, tc.data), tc.expected)
			if blocked(got) != tc.wantBlocked {
				t.Fatalf("UNSUPPORTED_ARCH=%v, want %v (%#v)", blocked(got), tc.wantBlocked, got)
			}
		})
	}
	explicit := inspect(t, write(t, arm64), "arm64")
	ambiguous := inspect(t, write(t, arm64), "")
	if explicit.ArtifactType != ambiguous.ArtifactType || !reflect.DeepEqual(explicit.ComputedDigests, ambiguous.ComputedDigests) || !reflect.DeepEqual(explicit.Blockers, ambiguous.Blockers) {
		t.Fatalf("ambiguous expectedArch changed the inspection: %#v vs %#v", explicit, ambiguous)
	}
}

func TestInspectArchiveNoExecutableAndTraversal(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	_ = z.Close()
	d := testDir(t)
	p := filepath.Join(d, "plain.zip")
	_ = os.WriteFile(p, buf.Bytes(), 0600)
	r, e := Inspect(context.Background(), Artifact{Path: p, Size: int64(buf.Len())}, archive.FormatZip, "")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, x := range r.Blockers {
		if x == "NO_EXECUTABLE" {
			found = true
		}
	}
	_ = found
	buf.Reset()
	z = zip.NewWriter(&buf)
	w, _ := z.Create("../../escape")
	_, _ = w.Write([]byte("x"))
	_ = z.Close()
	_ = os.WriteFile(p, buf.Bytes(), 0600)
	r, e = Inspect(context.Background(), Artifact{Path: p, Size: int64(buf.Len())}, archive.FormatZip, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Blockers) == 0 {
		t.Fatalf("traversal not blocked: %#v", r)
	}
}

func TestAPI403RateLimitClassification(t *testing.T) {
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		x := response(403, "")
		x.Header.Set("X-RateLimit-Remaining", "0")
		return x, nil
	})}}
	_, e := c.Discover(context.Background(), "O/R")
	var ae *APIError
	if !errors.As(e, &ae) || ae.Kind != APIErrorRateLimited {
		t.Fatalf("%v", e)
	}
}

func assertMalformedInspection(t *testing.T, format archive.Format, data []byte) {
	t.Helper()
	d := testDir(t)
	p := filepath.Join(d, "bad")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	r, e := Inspect(context.Background(), Artifact{Path: p, Size: int64(len(data))}, format, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Blockers) == 0 {
		t.Fatalf("malformed %s not blocked: %#v", format, r)
	}
}
func TestMalformedZIP(t *testing.T) {
	assertMalformedInspection(t, archive.FormatZip, []byte("not zip"))
}
func TestMalformedTarGZ(t *testing.T) {
	assertMalformedInspection(t, archive.FormatTarGz, []byte("not gzip"))
}
func TestMalformedTarXZ(t *testing.T) {
	assertMalformedInspection(t, archive.FormatTarXZ, []byte("not xz"))
}
func TestOversizedDownload(t *testing.T) {
	d := testDir(t)
	c := &Client{TempRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		x := response(200, "x")
		x.ContentLength = maxArtifactBytes + 1
		return x, nil
	})}}
	a := Asset{URL: "https://objects.example/x", Size: 2}
	if _, e := c.FetchUnverified(context.Background(), a); !errors.Is(e, download.ErrTooLarge) {
		t.Fatal("size mismatch accepted")
	}
}
func TestArchiveResourceLimits(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	long := strings.Repeat("a", archive.DefaultLimits().MaxPathBytes+1)
	w, _ := z.Create(long)
	_, _ = w.Write([]byte("x"))
	_ = z.Close()
	d := testDir(t)
	p := filepath.Join(d, "limited.zip")
	_ = os.WriteFile(p, b.Bytes(), 0600)
	r, e := Inspect(context.Background(), Artifact{Path: p, Size: int64(b.Len())}, archive.FormatZip, "")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Blockers) == 0 || r.Blockers[0] != "UNSUPPORTED_ARTIFACT" {
		t.Fatalf("%#v", r)
	}
}
func TestArchiveTraversal(t *testing.T) { TestInspectArchiveNoExecutableAndTraversal(t) }
func TestArchiveHardlinkAndSpecialFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  byte
	}{{"hardlink", tar.TypeLink}, {"fifo", tar.TypeFifo}} {
		var b bytes.Buffer
		g := gzip.NewWriter(&b)
		tw := tar.NewWriter(g)
		_ = tw.WriteHeader(&tar.Header{Name: tc.name, Typeflag: tc.typ, Linkname: "x"})
		_ = tw.Close()
		_ = g.Close()
		d := testDir(t)
		p := filepath.Join(d, tc.name+".tar.gz")
		_ = os.WriteFile(p, b.Bytes(), 0600)
		r, e := Inspect(context.Background(), Artifact{Path: p, Size: int64(b.Len())}, archive.FormatTarGz, "")
		if e != nil {
			t.Fatal(e)
		}
		if len(r.Blockers) == 0 || r.Blockers[0] != "UNSUPPORTED_ARTIFACT" {
			t.Fatalf("%s: %#v", tc.name, r)
		}
	}
}
func TestMalformedAppImage(t *testing.T) {
	d := testDir(t)
	p := filepath.Join(d, "bad.AppImage")
	_ = os.WriteFile(p, []byte("bad"), 0600)
	r, e := Inspect(context.Background(), Artifact{Path: p, Size: 3}, "appimage", "amd64")
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Blockers) == 0 {
		t.Fatal("malformed AppImage accepted")
	}
}

func TestVerifiedCacheHit(t *testing.T) {
	d := testDir(t)
	data := []byte("hit")
	sum := sha256.Sum256(data)
	calls := 0
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, string(data)), nil })}}
	a := Asset{ID: 1, ReleaseID: 1, Repository: "o/r", State: "uploaded", URL: "https://objects.example/x", Size: 3, Digest: "sha256:" + hex.EncodeToString(sum[:])}
	p := EvaluateProvenance("o/r", Release{ID: 1, Repository: "o/r"}, a)
	if _, err := c.Fetch(context.Background(), a, p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fetch(context.Background(), a, p); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("network=%d", calls)
	}
}
func TestUnchangedRefreshReusesAndRehashes(t *testing.T) {
	d := testDir(t)
	data := []byte("same")
	sum := sha256.Sum256(data)
	body := `[{"id":10,"tag_name":"v1","assets":[{"id":100,"name":"x","browser_download_url":"https://objects.example/x","size":4,"state":"uploaded","digest":"sha256:` + hex.EncodeToString(sum[:]) + `"}]}]`
	api, downloads := 0, 0
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "releases") {
			api++
			return response(200, body), nil
		}
		downloads++
		return response(200, string(data)), nil
	})}}
	rs, e := c.Discover(context.Background(), "O/R")
	if e != nil {
		t.Fatal(e)
	}
	a := rs[0].Assets[0]
	p := EvaluateProvenance("o/r", rs[0], a)
	if _, e = c.Fetch(context.Background(), a, p); e != nil {
		t.Fatal(e)
	}
	c.Refresh = true
	rs, e = c.Discover(context.Background(), "O/R")
	if e != nil {
		t.Fatal(e)
	}
	p = EvaluateProvenance("o/r", rs[0], rs[0].Assets[0])
	inspectParserHook = func() error { return nil }
	defer func() { inspectParserHook = nil }()
	if _, e = c.InspectAsset(context.Background(), rs[0].Assets[0], p, archive.FormatZip, ""); e != nil {
		t.Fatal(e)
	}
	if api != 2 || downloads != 1 {
		t.Fatalf("api=%d downloads=%d", api, downloads)
	}
}
func TestAcceptableToRejectedVerdictRecomputed(t *testing.T) {
	a := Asset{ID: 1, ReleaseID: 1, Repository: "o/r", State: "uploaded", Digest: "sha256:" + strings.Repeat("a", 64)}
	r := Release{ID: 1, Repository: "o/r"}
	if EvaluateProvenance("o/r", r, a).Verdict != Acceptable {
		t.Fatal("initial")
	}
	a.Digest = ""
	if EvaluateProvenance("o/r", r, a).ReasonCode != "NO_AUTHORITATIVE_DIGEST" {
		t.Fatal("cached verdict")
	}
}
func TestSameTagAsset100To200EndToEnd(t *testing.T) {
	testDiscoveryIdentityRefresh(t, 100, 200, 10, 10)
}
func TestRecreatedRelease10To20EndToEnd(t *testing.T) {
	testDiscoveryIdentityRefresh(t, 100, 100, 10, 20)
}
func testDiscoveryIdentityRefresh(t *testing.T, id1, id2, rel1, rel2 int64) {
	d := testDir(t)
	sum := sha256.Sum256([]byte("x"))
	b := func(id, rel int64) string {
		return fmt.Sprintf(`[{"id":%d,"tag_name":"v1","assets":[{"id":%d,"name":"x","browser_download_url":"https://objects.example/x","size":1,"state":"uploaded","digest":"sha256:%s"}]}]`, rel, id, hex.EncodeToString(sum[:]))
	}
	n := 0
	downloads := 0
	c := &Client{CacheRoot: d, APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "objects.example" {
			downloads++
			return response(200, "x"), nil
		}
		n++
		if n == 1 {
			return response(200, b(id1, rel1)), nil
		}
		return response(200, b(id2, rel2)), nil
	})}}
	rs, e := c.Discover(context.Background(), "O/R")
	if e != nil {
		t.Fatal(e)
	}
	a := rs[0].Assets[0]
	p := EvaluateProvenance("o/r", rs[0], a)
	if _, e = c.Fetch(context.Background(), a, p); e != nil {
		t.Fatal(e)
	}
	c.Refresh = true
	rs, e = c.Discover(context.Background(), "O/R")
	if e != nil {
		t.Fatal(e)
	}
	a2 := rs[0].Assets[0]
	p = EvaluateProvenance("o/r", rs[0], a2)
	if _, e = c.Fetch(context.Background(), a2, p); e != nil {
		t.Fatal(e)
	}
	if a.ID == a2.ID && a.ReleaseID == a2.ReleaseID {
		t.Fatal("identity did not change")
	}
	if downloads != 2 {
		t.Fatalf("downloads=%d", downloads)
	}
}
func TestExactTagTrailingJSON(t *testing.T) {
	c := &Client{APIBase: "https://api.example", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"id":1,"tag_name":"v","assets":[]} trailing`), nil
	})}}
	if _, e := c.DiscoverRelease(context.Background(), "O/R", "v"); e == nil {
		t.Fatal("trailing accepted")
	}
}

func TestProvenanceDoesNotNormalizeDigest(t *testing.T) {
	r := Release{ID: 1, Repository: "o/r"}
	a := Asset{ID: 2, ReleaseID: 1, Repository: "o/r", State: "uploaded", Digest: "sha256:" + strings.Repeat("A", 64)}
	p := EvaluateProvenance("o/r", r, a)
	if p.ReasonCode != "MALFORMED_DIGEST" {
		t.Fatalf("reason=%s", p.ReasonCode)
	}
	a.Digest = "sha1:" + strings.Repeat("a", 40)
	if EvaluateProvenance("o/r", r, a).ReasonCode != "UNSUPPORTED_DIGEST_ALGORITHM" {
		t.Fatal("algorithm")
	}
	a.Digest = ""
	if EvaluateProvenance("o/r", r, a).ReasonCode != "NO_AUTHORITATIVE_DIGEST" {
		t.Fatal("missing")
	}
}

func TestDiscoveryTTLRefreshAndValidatedCache(t *testing.T) {
	d := testDir(t)
	now := time.Unix(1000, 0)
	calls := 0
	body := `[{"id":10,"tag_name":"v1","url":"https://api.github.com/releases/10","assets":[{"id":100,"name":"x.zip","browser_download_url":"https://objects.example/x","size":3,"state":"uploaded","uploader":{"id":1},"digest":"sha256:` + strings.Repeat("a", 64) + `"}]}]`
	c := &Client{CacheRoot: d, APIBase: "https://api.example", Now: func() time.Time { return now }, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, body), nil })}}
	if _, e := c.Discover(context.Background(), "O/R"); e != nil {
		t.Fatal(e)
	}
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(500, "bad"), nil })}
	if _, e := c.Discover(context.Background(), "O/R"); e != nil {
		t.Fatal("cache miss", e)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	now = now.Add(DiscoveryTTL)
	if _, e := c.Discover(context.Background(), "O/R"); e == nil {
		t.Fatal("expired cache hid API failure")
	}
	c.Refresh = true
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, body), nil })}
	if _, e := c.Discover(context.Background(), "O/R"); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(d, "discovery")); e != nil {
		t.Fatal(e)
	}
}

func TestCorruptVerifiedCacheRedownloads(t *testing.T) {
	d := testDir(t)
	data := []byte("artifact")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	calls := 0
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, string(data)), nil })}}
	a := Asset{ID: 7, ReleaseID: 9, Repository: "o/r", Name: "x", URL: "https://objects.example/x", Size: int64(len(data)), State: "uploaded", Digest: "sha256:" + digest}
	p := EvaluateProvenance("o/r", Release{ID: 9, Repository: "o/r"}, a)
	x, e := c.Fetch(context.Background(), a, p)
	if e != nil {
		t.Fatal(e)
	}
	if err := os.WriteFile(x.Path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e = c.Fetch(context.Background(), a, p); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	if got, _ := os.ReadFile(x.Path); string(got) != string(data) {
		t.Fatal("bad bytes remained")
	}
}

func TestIdentityChangesProduceDistinctArtifactKeys(t *testing.T) {
	d := testDir(t)
	data := []byte("artifact")
	sum := sha256.Sum256(data)
	a0 := Asset{ID: 1, ReleaseID: 10, Repository: "o/r", State: "uploaded", Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(sum[:])}
	p := EvaluateProvenance("o/r", Release{ID: 10, Repository: "o/r"}, a0)
	client := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, string(data)), nil })}}
	a := Asset{ID: 1, ReleaseID: 10, Repository: "o/r", URL: "https://objects.example/a", Size: int64(len(data)), State: "uploaded", Digest: p.Algorithm + ":" + p.Digest}
	x, e := client.Fetch(context.Background(), a, p)
	if e != nil {
		t.Fatal(e)
	}
	b := a
	b.ID = 2
	pb := EvaluateProvenance("o/r", Release{ID: 10, Repository: "o/r"}, b)
	y, e := client.Fetch(context.Background(), b, pb)
	if e != nil {
		t.Fatal(e)
	}
	if x.Path == y.Path {
		t.Fatal("asset IDs shared cache")
	}
	c := a
	c.ReleaseID = 20
	pc := EvaluateProvenance("o/r", Release{ID: 20, Repository: "o/r"}, c)
	z, e := client.Fetch(context.Background(), c, pc)
	if e != nil {
		t.Fatal(e)
	}
	if x.Path == z.Path {
		t.Fatal("release IDs shared cache")
	}
}

func TestInspectReverifiesBeforeParserHook(t *testing.T) {
	d := testDir(t)
	p := filepath.Join(d, "artifact")
	if e := os.WriteFile(p, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	inspectParserHook = func() error { t.Fatal("parser reached corrupt bytes"); return nil }
	defer func() { inspectParserHook = nil }()
	data := []byte("good")
	sum := sha256.Sum256(data)
	a := Artifact{Path: p, Provenance: Provenance{Verdict: Acceptable, Algorithm: "sha256", Digest: hex.EncodeToString(sum[:])}}
	if _, e := Inspect(context.Background(), a, "", ""); e == nil {
		t.Fatal("corruption not rejected")
	}
}

func TestInspectComputesDigestsForUnverifiedArtifact(t *testing.T) {
	d := testDir(t)
	data := []byte("locally verified artifact")
	p := filepath.Join(d, "artifact")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(context.Background(), Artifact{Path: p, Size: int64(len(data))}, "", "")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	sha256Sum := sha256.Sum256(data)
	sha512Sum := sha512.Sum512(data)
	if got := inspection.ComputedDigests["sha256"]; got != hex.EncodeToString(sha256Sum[:]) {
		t.Fatalf("computed SHA-256 = %q", got)
	}
	if got := inspection.ComputedDigests["sha512"]; got != hex.EncodeToString(sha512Sum[:]) {
		t.Fatalf("computed SHA-512 = %q", got)
	}
}

func TestUnverifiedArtifactCleanup(t *testing.T) {
	d := testDir(t)
	data := []byte("raw")
	c := &Client{TempRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return response(200, string(data)), nil })}}
	a := Asset{URL: "https://objects.example/raw", Size: int64(len(data))}
	x, e := c.FetchUnverified(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	if !x.Temporary {
		t.Fatal("not temporary")
	}
	if e := x.Cleanup(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Dir(x.Path)); !os.IsNotExist(e) {
		t.Fatal("temporary bytes remain")
	}
	_ = d
}

func TestInspectAssetRedownloadsBeforeParser(t *testing.T) {
	d := testDir(t)
	data := []byte("fresh")
	sum := sha256.Sum256(data)
	a := Asset{ID: 3, ReleaseID: 4, Repository: "o/r", State: "uploaded", Size: int64(len(data)), Digest: "sha256:" + hex.EncodeToString(sum[:])}
	p := EvaluateProvenance("o/r", Release{ID: 4, Repository: "o/r"}, a)
	calls := 0
	c := &Client{CacheRoot: d, HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { calls++; return response(200, string(data)), nil })}}
	a.URL = "https://objects.example/a"
	x, e := c.Fetch(context.Background(), a, p)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(x.Path, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	parsed := 0
	inspectParserHook = func() error { parsed++; return nil }
	defer func() { inspectParserHook = nil }()
	_, e = c.InspectAsset(context.Background(), a, p, "", "")
	if e != nil && parsed == 0 {
		t.Fatalf("parse was not reached after redownload: %v", e)
	}
	if calls != 2 {
		t.Fatalf("downloads=%d", calls)
	}
	if parsed != 1 {
		t.Fatalf("parser calls=%d", parsed)
	}
}
