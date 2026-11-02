// Package privatecatalog implements the explicitly enrolled Recit authority.
// It is separate from the compiled public catalog and cannot enroll other apps.
package privatecatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/manifest"
)

const RepositoryID int64 = 1442652140
const Repository = "drobilica/recit"
const CatalogPath = "catalog/recit/manifest.yaml"
const apiRoot = "https://api.github.com/repos/" + Repository
const maxMetadata = 2 << 20

type Enrollment struct {
	Schema       int    `json:"schema"`
	RepositoryID int64  `json:"repository_id"`
	Repository   string `json:"repository"`
	Catalog      string `json:"catalog"`
	App          string `json:"app"`
}

func enrollment() Enrollment { return Enrollment{1, RepositoryID, Repository, CatalogPath, "recit"} }

type cached struct {
	Enrollment Enrollment `json:"enrollment"`
	Manifest   []byte     `json:"manifest"`
}
type Client struct {
	HTTP    *http.Client
	Token   string
	Offline bool
}

func New(httpClient *http.Client, offline bool) *Client {
	return &Client{HTTP: httpClient, Token: os.Getenv("TARLINK_PRIVATE_GITHUB_TOKEN"), Offline: offline}
}
func configPath(layout filesystem.Layout) string {
	return filepath.Join(filepath.Dir(layout.RepositoryConfig), "private-recit.json")
}
func cachePath(layout filesystem.Layout) string {
	return filepath.Join(layout.Cache, "private-recit", "catalog.json")
}
func (c *Client) request(ctx context.Context, path, accept string) ([]byte, error) {
	if c.Offline {
		return nil, errors.New("private metadata unavailable offline")
	}
	if c.Token == "" || strings.ContainsAny(c.Token, "\r\n") {
		return nil, errors.New("TARLINK_PRIVATE_GITHUB_TOKEN is required")
	}
	if c.HTTP == nil {
		return nil, errors.New("private HTTP client is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, "GET", apiRoot+path, nil)
	if err != nil {
		return nil, errors.New("invalid private API request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "TarLink-private-recit")
	client := *c.HTTP
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("private metadata redirects refused") }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("private GitHub metadata request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("private GitHub metadata HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, errors.New("private GitHub metadata read failed")
	}
	if len(b) > maxMetadata {
		return nil, errors.New("private metadata exceeds size bound")
	}
	return b, nil
}
func (c *Client) verifyRepository(ctx context.Context) error {
	b, err := c.request(ctx, "", "application/vnd.github+json")
	if err != nil {
		return err
	}
	var r struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if json.Unmarshal(b, &r) != nil || r.ID != RepositoryID || r.FullName != Repository || !r.Private {
		return errors.New("private repository identity or visibility does not match enrollment")
	}
	return nil
}
func (c *Client) Enroll(ctx context.Context, layout filesystem.Layout, id int64) error {
	if id != RepositoryID {
		return errors.New("only the explicitly approved Recit repository ID may be enrolled")
	}
	if err := c.verifyRepository(ctx); err != nil {
		return err
	}
	path := configPath(layout)
	if b, err := readPrivate(path); err == nil {
		var e Enrollment
		if decode(b, &e) != nil || e != enrollment() {
			return errors.New("existing private enrollment differs")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	b, _ := json.Marshal(enrollment())
	return writePrivate(path, b)
}
func LoadEnrollment(layout filesystem.Layout) error {
	b, err := readPrivate(configPath(layout))
	if err != nil {
		return fmt.Errorf("private Recit enrollment required: %w", err)
	}
	var e Enrollment
	if decode(b, &e) != nil || e != enrollment() {
		return errors.New("invalid private Recit enrollment")
	}
	return nil
}
func (c *Client) Resolve(ctx context.Context, layout filesystem.Layout, version string) (*manifest.Manifest, error) {
	if err := LoadEnrollment(layout); err != nil {
		return nil, err
	}
	var raw []byte
	if c.Offline {
		b, err := readPrivate(cachePath(layout))
		if err != nil {
			return nil, err
		}
		var v cached
		if decode(b, &v) != nil || v.Enrollment != enrollment() {
			return nil, errors.New("invalid private catalog cache authority")
		}
		raw = v.Manifest
	} else {
		if err := c.verifyRepository(ctx); err != nil {
			return nil, err
		}
		var err error
		raw, err = c.request(ctx, "/contents/"+CatalogPath+"?ref=main", "application/vnd.github.raw+json")
		if err != nil {
			return nil, err
		}
	}
	item, err := parse(raw, version)
	if err != nil {
		return nil, err
	}
	if !c.Offline {
		if err = c.verifyAsset(ctx, item); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(cached{enrollment(), raw})
		if err = writePrivate(cachePath(layout), b); err != nil {
			return nil, err
		}
	}
	return item, nil
}
func parse(raw []byte, version string) (*manifest.Manifest, error) {
	doc, err := manifest.ParseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("private catalog: %w", err)
	}
	if doc.ID != "recit" || len(doc.Platforms) != 1 {
		return nil, errors.New("private catalog may describe only recit linux-amd64")
	}
	item, err := doc.ResolvePlatform("linux-amd64")
	if err != nil {
		return nil, err
	}
	for _, r := range item.ReleaseHistory.Releases {
		if _, err := assetID(r.URL); err != nil {
			return nil, err
		}
		if r.RuntimeRef != nil || r.Runtime != nil || r.Archive != "tar.gz" || r.Verification.Algorithm != "sha256" || r.NestedArchive.Path != "" {
			return nil, errors.New("private Recit requires standalone SHA256 tar.gz artifacts")
		}
	}
	if item.Desktop.Enabled || len(item.Application.Executables) != 1 || item.Application.Executables[0].Name != "recit" || item.Application.Executables[0].Path != "recit" {
		return nil, errors.New("private Recit permits only the recit executable and no desktop integration")
	}
	if !item.WantsEnabled() {
		return nil, errors.New("private Recit is disabled")
	}
	if version != "" {
		found := false
		for _, r := range item.ReleaseHistory.Releases {
			if r.Version == version {
				v := item.SelectRelease(r)
				item = &v
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("requested private version is not retained")
		}
	}
	if len(item.Application.Executables) != 1 || item.Application.Executables[0].Name != "recit" || item.Application.Executables[0].Path != "recit" {
		return nil, errors.New("selected private release must execute only recit")
	}
	item.Authority = manifest.PrivateRecitAuthority
	return item, nil
}
func assetID(raw string) (int64, error) {
	u, err := url.Parse(raw)
	prefix := apiRoot + "/releases/assets/"
	if err != nil || !strings.HasPrefix(raw, prefix) || u.RawQuery != "" || u.Fragment != "" {
		return 0, errors.New("private artifacts must use exact enrolled GitHub API asset URLs")
	}
	v := strings.TrimPrefix(raw, prefix)
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != v {
		return 0, errors.New("invalid private release asset ID")
	}
	return id, nil
}
func (c *Client) verifyAsset(ctx context.Context, item *manifest.Manifest) error {
	id, err := assetID(item.Release.URL)
	if err != nil {
		return err
	}
	b, err := c.request(ctx, "/releases/assets/"+strconv.FormatInt(id, 10), "application/vnd.github+json")
	if err != nil {
		return err
	}
	var a struct {
		ID      int64  `json:"id"`
		Name    string `json:"name"`
		State   string `json:"state"`
		URL     string `json:"url"`
		Browser string `json:"browser_download_url"`
		Digest  string `json:"digest"`
	}
	version := item.Release.Version
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	name := "recit-" + version + "-linux-amd64.tar.gz"
	if json.Unmarshal(b, &a) != nil || a.ID != id || a.Name != name || a.State != "uploaded" || a.URL != item.Release.URL || a.Browser != "https://github.com/"+Repository+"/releases/download/"+version+"/"+name {
		return errors.New("private release asset identity mismatch")
	}
	if a.Digest != "" && a.Digest != "sha256:"+item.Release.Verification.Digest {
		return errors.New("private GitHub asset digest differs from catalog")
	}
	return nil
}

// AssetTransport attaches a token only to one validated API asset request.
// Redirect storage URLs receive no credentials and only GitHub's exact release
// storage host is allowed. The normal downloader still enforces size/digest.
func (c *Client) AssetTransport(item *manifest.Manifest) http.RoundTripper {
	base := c.HTTP.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &assetTransport{base: base, asset: item.Release.URL, token: c.Token}
}

type assetTransport struct {
	base         http.RoundTripper
	asset, token string
}

func (t *assetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	copy.Header.Del("Authorization")
	copy.Header.Del("Cookie")
	if req.URL.String() == t.asset {
		copy.Header.Set("Authorization", "Bearer "+t.token)
		copy.Header.Set("Accept", "application/octet-stream")
		copy.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	} else if req.URL.Scheme != "https" || req.URL.Host != "release-assets.githubusercontent.com" || req.URL.User != nil || req.URL.Fragment != "" {
		return nil, errors.New("private artifact redirect is outside approved GitHub storage")
	}
	resp, err := t.base.RoundTrip(copy)
	if err != nil {
		return nil, errors.New("private artifact network request failed")
	}
	return resp, nil
}
func decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing private data")
	}
	return nil
}
func readPrivate(path string) ([]byte, error) {
	if err := filesystem.CheckOwnedDirectoryWithin("/", filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > maxMetadata {
		return nil, errors.New("private metadata must be a private bounded regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return nil, errors.New("private metadata is not user-owned")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxMetadata+1))
	if len(b) > maxMetadata {
		return nil, errors.New("private metadata exceeds size bound")
	}
	return b, err
}
func writePrivate(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := filesystem.SecureMkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := filesystem.CheckOwnedDirectory(dir); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if _, err = readPrivate(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".private-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
