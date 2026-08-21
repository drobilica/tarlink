package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

func TestCheckTarLinkVersionFreshReportsNewRelease(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"tag_name":"v2.0.0","assets":[{"name":"tarlink-linux-amd64","browser_download_url":"https://example.test/binary"},{"name":"checksums.txt","browser_download_url":"https://example.test/checksums"}]}]`))
	}))
	defer server.Close()

	transport := &rewriteUpgradeTransport{base: server.Client().Transport, target: server.URL}
	core, err := NewCore(filesystem.Layout{Home: dir, Cache: dir}, &download.Client{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	core.upgrader.Current = "1.0.0"
	core.upgrader.GOARCH = "amd64"
	value, err := core.CheckTarLinkVersionFresh(context.Background())
	if err != nil || value.Latest != "2.0.0" || !value.UpgradeAvailable {
		t.Fatalf("value=%#v err=%v", value, err)
	}
}

// Keep the production upgrade service pointed at its fixed GitHub URL while
// routing this test's requests to the local TLS server.
type rewriteUpgradeTransport struct {
	base   http.RoundTripper
	target string
}

func (t *rewriteUpgradeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	target, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
	return t.base.RoundTrip(clone)
}
