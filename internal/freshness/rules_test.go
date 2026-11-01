package freshness

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestResolveExplainsUncoveredSourcesWithoutApprovingThem(t *testing.T) {
	for _, tc := range []struct {
		urls   []string
		reason string
	}{
		{nil, "missing_upstream"},
		{[]string{"https://gitlab.com/owner/project/-/package_files/123/download"}, "unsupported_provider"},
		{[]string{"https://downloads.example.org/app.tar.gz"}, "unsupported_provider"},
		{[]string{"https://github.com/owner/new/releases/download/v1/app.zip"}, "unreviewed_repository"},
		{[]string{"https://github.com/one/repo/releases/download/v1/app.zip", "https://github.com/two/repo/releases/download/v1/app.zip"}, "ambiguous_upstream"},
		{[]string{"http://github.com/owner/new/releases/download/v1/app.zip"}, "invalid_upstream"},
		{[]string{"https://user:password@github.com/owner/new/releases/download/v1/app.zip"}, "invalid_upstream"},
		{[]string{"https://github.com.evil.example/owner/new"}, "unsupported_provider"},
	} {
		got, err := Resolve("unknown-app", tc.urls)
		if err != nil || got.Repository != "" || got.Rule != "" || got.Reason != tc.reason || got.Detail == "" {
			t.Fatalf("urls=%v got=%+v err=%v", tc.urls, got, err)
		}
	}
	got, err := Resolve("banjo-recompiled", []string{"https://evil.invalid/other/source"})
	if err != nil || got.Repository != "BanjoRecomp/BanjoRecomp" || got.Rule != "reviewed_github_releases" {
		t.Fatalf("manifest overrode compiled rule: %+v %v", got, err)
	}
}

func TestReviewedVersionAndAssetRules(t *testing.T) {
	for _, tc := range []struct{ repo, tag, want string }{
		{"sciaschi/CBFD-Recompiled", "V0.1.7", "0.1.7"},
		{"ThatGuyMcd/DKR-R", "Version1.0.7", "1.0.7"},
		{"LostArtefacts/TRX", "trx-1.12.0", "1.12.0"},
		{"godotengine/godot-builds", "4.8-stable", "4.8"},
		{"other/project", "Version1.0.7", "Version1.0.7"},
		{"LostArtefacts/TRX", "trx-nightly", "trx-nightly"},
		{"OpenRA/OpenRA", "release-20250330", "release-20250330"},
	} {
		if got := ReleaseVersion(tc.repo, tc.tag); got != tc.want {
			t.Fatalf("%s %s = %s", tc.repo, tc.tag, got)
		}
	}
	if !MatchesAsset("ace-driver", "AceDriver-x86_64.AppImage") || MatchesAsset("ace-driver", "ArmadilloRacing-x86_64.AppImage") || MatchesAsset("openra-red-alert", "OpenRA-Dune-2000-x86_64.AppImage") {
		t.Fatal("shared repository asset rule failed")
	}
	for _, data := range []string{
		"- app: a\n  repository: owner/repo\n  version-prefix: '../'\n",
		"- app: a\n  repository: owner/repo\n  version-prefix: V\n- app: b\n  repository: owner/repo\n  version-prefix: Version\n",
		"- app: a\n  repository: owner/repo\n  arbitrary-command: nope\n",
	} {
		if _, err := LoadRules([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestScanMemoizesRepositoriesButNotFilteringOrFutureScans(t *testing.T) {
	calls := 0
	base := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[{"tag_name":"v2.0"},{"tag_name":"v3.0","prerelease":true}]`))}, nil
	})}}
	scan := base.ForScan()
	targets := []Target{
		{App: "one", Repository: "owner/repo", Channel: "stable", Current: "1.0"},
		{App: "two", Repository: "owner/repo", Channel: "stable", Current: "2.0"},
		{App: "one", Repository: "owner/repo", Channel: "nightly", Current: "1.0"},
	}
	for i, target := range targets {
		got, err := scan.Discover(t.Context(), target)
		want := 1
		if i == 1 {
			want = 0
		}
		if err != nil || len(got) != want {
			t.Fatalf("target=%+v got=%+v err=%v", target, got, err)
		}
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	if _, err := base.ForScan().Discover(t.Context(), targets[0]); err != nil || calls != 2 {
		t.Fatalf("new scan reused stale metadata: %d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := scan.Discover(ctx, targets[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached cancellation=%v", err)
	}
}

func TestConcurrentScanLookupAndStructuredFailures(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{301, "repository_moved"}, {404, "repository_not_found"}, {429, "rate_limited"}, {401, "authentication_failed"}} {
		t.Run(tc.reason, func(t *testing.T) {
			calls := 0
			client := (&Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}}).ForScan()
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					_, err := client.Discover(t.Context(), Target{App: "demo", Repository: "owner/repo", Channel: "stable"})
					var issue *Issue
					if !errors.As(err, &issue) || issue.Reason != tc.reason {
						t.Errorf("err=%v", err)
					}
				})
			}
			wg.Wait()
			if calls != 1 {
				t.Fatalf("repeated failing source %d times", calls)
			}
		})
	}
}

func TestDiscoveryCredentialCannotFollowRedirect(t *testing.T) {
	t.Setenv("GH_TOKEN", "fixture-secret")
	calls := 0
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Fatal("credential missing or sent outside GitHub API")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://evil.invalid/steal"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}}
	_, err := client.Discover(t.Context(), Target{App: "demo", Repository: "owner/repo", Channel: "stable"})
	var issue *Issue
	if calls != 1 || !errors.As(err, &issue) || issue.Reason != "repository_moved" || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
