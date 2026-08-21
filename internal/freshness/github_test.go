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
		if r.URL.Path != "/repos/example/project/releases" || r.URL.Query().Get("per_page") != "100" {
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
