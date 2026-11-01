package freshness

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewerReleaseComparison(t *testing.T) {
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	for _, test := range []struct {
		version, current string
		want             bool
	}{
		{"v1.10.0", "1.9.0", true}, {"1.9", "1.10", false}, {"1.0", "1.0.0", false}, {"v2.7.519", "2.7.518", true}, {"01.02", "1.2", false}, {"9999999999999999999999999", "2", true}, {"1.0", "2.0", false},
	} {
		got, err := IsNewerRelease(test.version, test.current, newer, old)
		if err != nil || got != test.want {
			t.Fatalf("%s > %s = %v, %v", test.version, test.current, got, err)
		}
	}
	if got, err := IsNewerRelease("beta-new", "beta-old", newer, old); err != nil || !got {
		t.Fatalf("opaque newer=%v err=%v", got, err)
	}
	if got, err := IsNewerRelease("beta-new", "beta-old", old, newer); err != nil || got {
		t.Fatalf("opaque older=%v err=%v", got, err)
	}
	if _, err := IsNewerRelease("beta-new", "beta-old", newer, time.Time{}); err == nil {
		t.Fatal("opaque comparison guessed without baseline")
	}
}

func TestDiscoverCurrentExcludesHistoricalReleases(t *testing.T) {
	client := &Client{HTTP: releaseFixtureHTTP(`[
 {"tag_name":"v1.9","published_at":"2026-02-01T00:00:00Z"},
 {"tag_name":"v1.10","published_at":"2026-01-01T00:00:00Z"},
 {"tag_name":"v1.11","published_at":"2026-03-01T00:00:00Z"}]`)}
	result, err := client.Discover(t.Context(), Target{App: "demo", Repository: "example/project", Channel: "stable", Current: "1.10"})
	if err != nil || len(result) != 1 || result[0].Version != "1.11" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func releaseFixtureHTTP(body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}
