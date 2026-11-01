package freshness

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoverReportsOnlyUnapprovedStableReleases(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/repos/example/project/releases" || r.URL.Query().Get("per_page") != "50" {
			return nil, fmt.Errorf("unexpected request %s", r.URL.String())
		}
		if r.Header.Get("Accept") == "" || r.Header.Get("User-Agent") == "" {
			return nil, errors.New("provider headers missing")
		}
		body := `[
 {"tag_name":"v2.0.0","html_url":"https://github.com/example/project/releases/tag/v2.0.0","draft":false,"prerelease":false},
 {"tag_name":"v1.0.0","html_url":"https://github.com/example/project/releases/tag/v1.0.0","draft":false,"prerelease":false},
 {"tag_name":"draft","draft":true,"prerelease":false},
	 {"tag_name":"preview","draft":false,"prerelease":true}
]`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	result, err := (&Client{HTTP: client}).Discover(context.Background(), Target{App: "demo", Repository: "example/project", Channel: "stable", Approved: []ApprovedRelease{{Version: "v1.0.0", Channel: "stable"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Version != "2.0.0" || result[0].Prerelease {
		t.Fatalf("candidates = %#v", result)
	}
}

func TestDiscoverNightlyReportsOnlyPrereleasesAndNormalizesTags(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `[
 {"tag_name":"v2.7.519","html_url":"https://github.com/example/project/releases/tag/v2.7.519","draft":false,"prerelease":true},
 {"tag_name":"v2.7.518","html_url":"https://github.com/example/project/releases/tag/v2.7.518","draft":false,"prerelease":true},
 {"tag_name":"v2.7.517","html_url":"https://github.com/example/project/releases/tag/v2.7.517","draft":false,"prerelease":false}
]`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	result, err := (&Client{HTTP: client}).Discover(context.Background(), Target{App: "demo", Repository: "example/project", Channel: "nightly", Approved: []ApprovedRelease{{Version: "2.7.518", Channel: "nightly"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 1 || result[0].Version != "2.7.519" || !result[0].Prerelease {
		t.Fatalf("candidates = %#v", result)
	}
}

func TestDiscoverDoesNotConflateOpaqueVPrefixedVersions(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `[{"tag_name":"v1","html_url":"https://github.com/example/project/releases/tag/v1","draft":false,"prerelease":false}]`
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	result, err := (&Client{HTTP: client}).Discover(context.Background(), Target{App: "demo", Repository: "example/project", Channel: "stable", Approved: []ApprovedRelease{{Version: "v1", Channel: "stable"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 0 {
		t.Fatalf("v-prefixed approved release was reported: %#v", result)
	}
}

func TestDiscoverRejectsInvalidTargetBeforeNetwork(t *testing.T) {
	client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("network request"); return nil, nil })}}
	if _, err := client.Discover(context.Background(), Target{App: "demo", Repository: "example/project/evil", Channel: "stable"}); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

func TestAPIURLIsFixedToGitHub(t *testing.T) {
	value, err := APIURL("owner/repo")
	if err != nil || value != "https://api.github.com/repos/owner/repo/releases" {
		t.Fatalf("url=%q err=%v", value, err)
	}
	if _, err := APIURL("https://evil.invalid/owner/repo"); err == nil {
		t.Fatal("external provider accepted")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDiscoverGodotStableBuildTags(t *testing.T) {
	client := &Client{HTTP: releaseFixtureHTTP(`[
 {"tag_name":"4.8-stable"}, {"tag_name":"4.7.2-stable"},
 {"tag_name":"4.7.1-stable"}, {"tag_name":"4.9-beta1","prerelease":true}]`)}
	got, err := client.Discover(t.Context(), Target{App: "godot", Repository: "godotengine/godot-builds", Channel: "stable", Current: "4.7.2", Approved: []ApprovedRelease{{Version: "4.7.2", Channel: "stable"}}})
	if err != nil || len(got) != 1 || got[0].Version != "4.8" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	for _, tag := range []string{"nightly-stable", "4.8-beta1", "4..8-stable"} {
		if got := ReleaseVersion("godotengine/godot-builds", tag); got != tag {
			t.Fatalf("changed opaque tag %q to %q", tag, got)
		}
	}
	if got := ReleaseVersion("other/project", "4.8-stable"); got != "4.8-stable" {
		t.Fatal(got)
	}
}

func TestDiscoverBoundedPagination(t *testing.T) {
	for _, failure := range []string{"", "http", "size", "json", "count", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			client := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Query().Get("page") != fmt.Sprint(calls) || r.URL.Query().Get("per_page") != "50" {
					t.Fatal(r.URL)
				}
				if err := r.Context().Err(); err != nil {
					return nil, err
				}
				entries := make([]string, 50)
				for i := range entries {
					entries[i] = fmt.Sprintf(`{"tag_name":"%d.0"}`, calls*50+i)
				}
				body := "[" + strings.Join(entries, ",") + "]"
				code := http.StatusOK
				if calls == 2 {
					switch failure {
					case "http":
						code = http.StatusTooManyRequests
					case "size":
						body = strings.Repeat("x", maxResponseBytes+1)
					case "json":
						body = "["
					case "count":
						body = "[" + strings.Join(append(entries, entries[0]), ",") + "]"
					}
				}
				if calls == 1 && failure == "cancel" {
					cancel()
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}}
			got, err := client.Discover(ctx, Target{App: "demo", Repository: "example/project", Channel: "stable", Current: "1.0"})
			if failure == "" {
				if err != nil || len(got) != 100 || calls != 2 {
					t.Fatalf("len=%d calls=%d err=%v", len(got), calls, err)
				}
			} else if err == nil || len(got) != 0 || calls > 2 {
				t.Fatalf("len=%d calls=%d err=%v", len(got), calls, err)
			}
		})
	}
}
