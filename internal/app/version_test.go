package app

import (
	"context"
	"net/http"
	"net/http/httptest"
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

	core, err := NewCore(filesystem.Layout{Home: dir, Cache: dir}, &download.Client{HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	core.upgrader.Current = "1.0.0"
	core.upgrader.GOARCH = "amd64"
	core.upgrader.APIURL = server.URL + "/releases"
	value, err := core.CheckTarLinkVersionFresh(context.Background())
	if err != nil || value.Latest != "2.0.0" || !value.UpgradeAvailable {
		t.Fatalf("value=%#v err=%v", value, err)
	}
}
