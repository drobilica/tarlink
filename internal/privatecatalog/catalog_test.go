package privatecatalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/manifest"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const fixture = `schema: 5
id: recit
name: Recit
summary: Private recovery research
homepage: https://github.com/drobilica/recit
categories: [development]
release:
  current: v0.1.0
  archive: tar.gz
  verification:
    algorithm: sha256
  releases:
    - version: v0.1.0
      artifacts:
        linux-amd64:
          url: https://api.github.com/repos/drobilica/recit/releases/assets/123
          verification:
            digest: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
            source: https://github.com/drobilica/recit/releases/tag/v0.1.0
application:
  executable:
    name: recit
    path: recit
`

func layout(t *testing.T) filesystem.Layout {
	t.Helper()
	l, err := filesystem.LayoutFor(t.TempDir(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func TestEnrollmentCatalogOfflineAndAuthority(t *testing.T) {
	l := layout(t)
	calls := 0
	c := &Client{Token: "never-print-this-token", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer never-print-this-token" {
			t.Fatal("metadata lost authentication")
		}
		switch r.URL.Path {
		case "/repos/drobilica/recit":
			return response(`{"id":1442652140,"full_name":"drobilica/recit","private":true}`), nil
		case "/repos/drobilica/recit/contents/catalog/recit/manifest.yaml":
			return response(fixture), nil
		case "/repos/drobilica/recit/releases/assets/123":
			return response(`{"id":123,"state":"uploaded","name":"recit-v0.1.0-linux-amd64.tar.gz","url":"https://api.github.com/repos/drobilica/recit/releases/assets/123","browser_download_url":"https://github.com/drobilica/recit/releases/download/v0.1.0/recit-v0.1.0-linux-amd64.tar.gz"}`), nil
		}
		return nil, errors.New("unexpected request")
	})}}
	if _, err := c.Resolve(context.Background(), l, ""); err == nil {
		t.Fatal("unenrolled resolution")
	}
	if err := c.Enroll(context.Background(), l, RepositoryID+1); err == nil || calls != 0 {
		t.Fatal("wrong enrollment contacted network")
	}
	if err := c.Enroll(context.Background(), l, RepositoryID); err != nil {
		t.Fatal(err)
	}
	item, err := c.Resolve(context.Background(), l, "")
	if err != nil {
		t.Fatal(err)
	}
	if item.Authority != manifest.PrivateRecitAuthority {
		t.Fatal("authority missing")
	}
	fp, _ := item.ResolvedPackageFingerprint()
	item.Authority = ""
	official, _ := item.ResolvedPackageFingerprint()
	if fp == official {
		t.Fatal("authority excluded from fingerprint")
	}
	prior := calls
	c.Offline = true
	c.Token = ""
	if _, err = c.Resolve(context.Background(), l, "v0.1.0"); err != nil {
		t.Fatal(err)
	}
	if calls != prior {
		t.Fatal("offline made network request")
	}
	if err = os.Chmod(configPath(l), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Resolve(context.Background(), l, ""); err == nil {
		t.Fatal("public-readable enrollment accepted")
	}
}
func TestRejectWrongIdentityAndMetadataRedirect(t *testing.T) {
	for _, body := range []string{`{"id":1,"full_name":"drobilica/recit","private":true}`, `{"id":1442652140,"full_name":"other/recit","private":true}`, `{"id":1442652140,"full_name":"drobilica/recit","private":false}`, strings.Repeat(" ", maxMetadata+1)} {
		c := &Client{Token: "secret", HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return response(body), nil })}}
		if err := c.Enroll(context.Background(), layout(t), RepositoryID); err == nil {
			t.Fatal("bad identity accepted")
		}
	}
	c := &Client{Token: "secret", HTTP: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		r := response("")
		r.StatusCode = 302
		r.Header.Set("Location", "https://evil.invalid/?secret=value")
		return r, nil
	})}}
	if err := c.Enroll(context.Background(), layout(t), RepositoryID); err == nil || strings.Contains(err.Error(), "evil") || strings.Contains(err.Error(), "value") {
		t.Fatal("metadata redirect not safely rejected", err)
	}
}
func TestStrictScopeAndURLs(t *testing.T) {
	for _, s := range []string{
		strings.Replace(fixture, "id: recit", "id: official-app", 1),
		strings.Replace(fixture, "releases/assets/123", "releases/assets/123?token=secret", 1),
		strings.Replace(fixture, "repos/drobilica/recit/releases/assets/123", "repos/other/recit/releases/assets/123", 1),
		strings.Replace(fixture, "releases/assets/123", "releases/assets/0123", 1),
		strings.Replace(fixture, "    path: recit", "    path: another", 1),
		strings.Replace(fixture, "linux-amd64:", "linux-arm64:", 1),
		strings.Replace(fixture, "  archive: tar.gz", "  archive: zip", 1),
		fixture + "authority: arbitrary\n",
	} {
		if _, err := parse([]byte(s), ""); err == nil {
			t.Fatal("unsafe manifest accepted", s)
		}
	}
}
func TestAssetTransportNeverForwardsCredentials(t *testing.T) {
	item, err := parse([]byte(fixture), "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := &Client{Token: "SECRET", HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "api.github.com" {
			if r.Header.Get("Authorization") != "Bearer SECRET" {
				t.Fatal("API auth missing")
			}
		} else if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("credentials leaked")
		}
		return response(""), nil
	})}}
	transport := c.AssetTransport(item)
	for _, u := range []string{item.Release.URL, "https://release-assets.githubusercontent.com/github-production-release-asset/123?sig=private"} {
		r, _ := http.NewRequest("GET", u, nil)
		r.Header.Set("Authorization", "Bearer injected")
		r.Header.Set("Cookie", "secret=yes")
		resp, err := transport.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for _, u := range []string{"https://evil.invalid/", "https://api.github.com/repos/other/thing/releases/assets/123", "http://release-assets.githubusercontent.com/file", "https://release-assets.githubusercontent.com:444/file"} {
		r, _ := http.NewRequest("GET", u, nil)
		if _, err = transport.RoundTrip(r); err == nil {
			t.Fatal("unsafe transport accepted", u)
		}
	}
	if calls != 2 {
		t.Fatal("rejected URL reached transport")
	}
}
func TestPrivateFilesRejectSymlinksAndForeignEnrollment(t *testing.T) {
	l := layout(t)
	if err := writePrivate(configPath(l), []byte(`{"schema":1,"repository_id":1,"repository":"drobilica/recit","catalog":"catalog/recit/manifest.yaml","app":"recit"}`)); err != nil {
		t.Fatal(err)
	}
	if LoadEnrollment(l) == nil {
		t.Fatal("foreign enrollment accepted")
	}
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(configPath(l)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, configPath(l)); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(enrollment())
	if writePrivate(configPath(l), b) == nil {
		t.Fatal("symlink overwritten")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "secret" {
		t.Fatal("external file changed")
	}
}
