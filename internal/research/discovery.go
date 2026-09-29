package research

// This file contains the disposable, metadata-only external catalog report.
// It deliberately has no path into registry admission or installation.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
)

const DiscoverySchemaVersion = 1
const maxCatalogMetadataBytes int64 = 4 << 20

var repositorySegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var nonLinuxAssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(windows|win|win32|win64|android|ios|macos|darwin|osx)([^a-z0-9]|$)`)
var linuxAssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])linux([^a-z0-9]|$)`)
var armhfAssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(armhf|armv7)([^a-z0-9]|$)`)
var i386AssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(i[3-6]86)([^a-z0-9]|$)`)
var arm64AssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(arm64|aarch64)([^a-z0-9]|$)`)
var amd64AssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(amd64|x86[-_.]?64|x64)([^a-z0-9]|$)`)
var x86AssetPattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])x86([^a-z0-9]|$)`)
var unsupportedAssetArchitecturePattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])(arm32|armv6l?|riscv64|ppc64(?:le)?|s390x|mips64el|mips64|mipsel|mips|loongarch64)([^a-z0-9]|$)`)

type discoveryTransport struct{ base http.RoundTripper }

func (t discoveryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.User != nil || req.URL.Scheme != "https" {
		return nil, errors.New("unsafe discovery HTTP URL")
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

type IdentityResolver func(context.Context, string, string) (string, error)
type DiscoveryOptions struct {
	Catalog, Registry string
	Changed           bool
	HTTP              *http.Client
	ResolveIdentity   IdentityResolver
}
type DiscoveryReport struct {
	SchemaVersion int               `json:"schema_version"`
	Catalog       DiscoveryCatalog  `json:"catalog"`
	Registry      DiscoveryRegistry `json:"registry"`
	Changed       bool              `json:"changed"`
	Summary       DiscoverySummary  `json:"summary"`
	Entries       []DiscoveryEntry  `json:"entries"`
}
type DiscoveryCatalog struct {
	Adapter  string `json:"adapter"`
	Source   string `json:"source"`
	Revision string `json:"revision"`
}
type DiscoveryRegistry struct {
	Source string `json:"source"`
}
type DiscoverySummary struct {
	New            int `json:"new"`
	ReleaseDiff    int `json:"release_diff"`
	RegisteredSame int `json:"registered_same"`
	Unsupported    int `json:"unsupported"`
	Ambiguous      int `json:"ambiguous"`
	NoLinux        int `json:"no_linux"`
}
type DiscoveryEntry struct {
	ExternalID         string                   `json:"external_id"`
	Name               string                   `json:"name"`
	Category           string                   `json:"category"`
	Project            string                   `json:"project,omitempty"`
	Provider           string                   `json:"provider"`
	Repository         string                   `json:"repository"`
	CatalogRelease     string                   `json:"catalog_release"`
	CatalogReleases    []string                 `json:"catalog_releases,omitempty"`
	Architectures      []string                 `json:"architectures"`
	LinuxAssets        []string                 `json:"linux_assets"`
	Preferred          DiscoveryAssetGroup      `json:"preferred"`
	Alternatives       []DiscoveryAssetGroup    `json:"alternatives"`
	ThumbnailURL       string                   `json:"thumbnail_url"`
	SelectionRevision  int                      `json:"selection_revision"`
	ReleaseAssetFilter string                   `json:"release_asset_filter,omitempty"`
	RegistryMatches    []DiscoveryRegistryMatch `json:"registry_matches"`
	Status             string                   `json:"status"`
	Reasons            []string                 `json:"reasons,omitempty"`
}
type DiscoveryAssetGroup struct {
	Format string   `json:"format"`
	Assets []string `json:"assets"`
}
type DiscoveryRegistryMatch struct {
	ID      string `json:"id"`
	Release string `json:"release"`
}

type catalogReader interface {
	Read(context.Context, string) ([]byte, error)
	Revision() string
}
type localCatalogReader struct{ root string }

func (r localCatalogReader) Revision() string { return "" }
func (r localCatalogReader) Read(_ context.Context, name string) ([]byte, error) {
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, errors.New("unsafe catalog path")
		}
	}
	root, err := filepath.Abs(r.root)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("catalog root must be a real directory")
	}
	cur := root
	for _, p := range parts {
		cur = filepath.Join(cur, filepath.FromSlash(p))
		info, e := os.Lstat(cur)
		if e != nil {
			return nil, e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("catalog path contains symlink")
		}
	}
	info, err := os.Stat(cur)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("catalog metadata is not a regular file")
	}
	if info.Size() > maxCatalogMetadataBytes {
		return nil, errors.New("catalog metadata too large")
	}
	f, err := os.Open(cur)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCatalogMetadataBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxCatalogMetadataBytes {
		return nil, errors.New("catalog metadata too large")
	}
	return b, nil
}

type remoteCatalogReader struct {
	client                *Client
	owner, repo, revision string
}

func (r remoteCatalogReader) Revision() string { return r.revision }
func (r remoteCatalogReader) Read(ctx context.Context, name string) ([]byte, error) {
	if _, err := safeCatalogPath(name); err != nil {
		return nil, err
	}
	endpoint := "https://api.github.com/repos/" + r.owner + "/" + r.repo + "/contents/" + strings.ReplaceAll(name, " ", "%20") + "?ref=" + url.QueryEscape(r.revision)
	var value struct {
		Type     string `json:"type"`
		Encoding string `json:"encoding"`
		Content  string `json:"content"`
	}
	if err := r.client.getAPIJSON(ctx, endpoint, &value); err != nil {
		return nil, err
	}
	if value.Type != "file" || value.Encoding != "base64" {
		return nil, errors.New("catalog metadata is not a base64 file")
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(value.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode catalog metadata: %w", err)
	}
	if int64(len(b)) > maxCatalogMetadataBytes {
		return nil, errors.New("catalog metadata too large")
	}
	return b, nil
}

func Discover(ctx context.Context, o DiscoveryOptions) (DiscoveryReport, error) {
	adapter, source, ok := strings.Cut(strings.TrimSpace(o.Catalog), ":")
	if !ok || adapter != "quiver" || source == "" {
		return DiscoveryReport{}, errors.New("catalog must be quiver:<source>")
	}
	if strings.Contains(source, "://") && !strings.HasPrefix(source, "https://github.com/") {
		return DiscoveryReport{}, errors.New("catalog source must be a local path or HTTPS GitHub repository")
	}
	if strings.TrimSpace(o.Registry) == "" {
		return DiscoveryReport{}, errors.New("registry root is required")
	}
	registryRoot, err := filepath.Abs(o.Registry)
	if err != nil {
		return DiscoveryReport{}, err
	}
	info, err := os.Stat(registryRoot)
	if err != nil || !info.IsDir() {
		return DiscoveryReport{}, errors.New("registry root is unavailable")
	}
	var reader catalogReader
	httpClient := o.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clone := *httpClient
	if clone.Timeout <= 0 || clone.Timeout > 30*time.Second {
		clone.Timeout = 30 * time.Second
	}
	clone.Transport = discoveryTransport{base: clone.Transport}
	httpClient = &clone
	identityClient := &Client{HTTP: httpClient}
	resolver := remoteIdentityResolver(identityClient)
	if strings.HasPrefix(source, "https://github.com/") {
		owner, repo, err := githubSource(source)
		if err != nil {
			return DiscoveryReport{}, err
		}
		client := identityClient
		var meta struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := client.getAPIJSON(ctx, "https://api.github.com/repos/"+owner+"/"+repo, &meta); err != nil {
			return DiscoveryReport{}, fmt.Errorf("Quiver repository: %w", err)
		}
		if meta.DefaultBranch == "" || strings.ContainsAny(meta.DefaultBranch, "\r\n") {
			return DiscoveryReport{}, errors.New("Quiver default branch is invalid")
		}
		var ref struct {
			Object struct {
				SHA  string `json:"sha"`
				Type string `json:"type"`
			} `json:"object"`
		}
		if err := client.getAPIJSON(ctx, "https://api.github.com/repos/"+owner+"/"+repo+"/git/ref/heads/"+url.PathEscape(meta.DefaultBranch), &ref); err != nil {
			return DiscoveryReport{}, fmt.Errorf("Quiver revision: %w", err)
		}
		if ref.Object.Type != "commit" || len(ref.Object.SHA) != 40 || strings.Trim(ref.Object.SHA, "0123456789abcdefABCDEF") != "" {
			return DiscoveryReport{}, errors.New("Quiver revision is not an immutable commit")
		}
		reader = remoteCatalogReader{client: client, owner: owner, repo: repo, revision: strings.ToLower(ref.Object.SHA)}
		resolver = remoteIdentityResolver(client)
	} else {
		reader = localCatalogReader{root: source}
	}
	if o.ResolveIdentity != nil {
		resolver = o.ResolveIdentity
	}
	report, err := discoverQuiver(ctx, reader, source, registryRoot, o.Changed, resolver)
	if err != nil {
		return DiscoveryReport{}, err
	}
	// Keep the caller's explicit comparison source in provenance; validation
	// uses the absolute path above only to enforce the registry boundary.
	report.Registry.Source = strings.TrimSpace(o.Registry)
	return report, nil
}

func remoteIdentityResolver(client *Client) IdentityResolver {
	return func(ctx context.Context, provider, repo string) (string, error) {
		switch provider {
		case "github":
			var value struct {
				FullName string `json:"full_name"`
			}
			if err := client.getAPIJSON(ctx, "https://api.github.com/repos/"+repo, &value); err != nil {
				return "", err
			}
			return normalizeRepository(provider, value.FullName)
		case "gitlab":
			endpoint := "https://gitlab.com/api/v4/projects/" + url.PathEscape(repo)
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			if err != nil {
				return "", err
			}
			transport := *client.httpClient()
			origin, _ := url.Parse(endpoint)
			transport.CheckRedirect = func(r *http.Request, via []*http.Request) error {
				if len(via) >= 3 || r.URL.Scheme != origin.Scheme || !strings.EqualFold(r.URL.Host, origin.Host) {
					return errors.New("GitLab identity redirect rejected")
				}
				return nil
			}
			resp, err := transport.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return "", fmt.Errorf("GitLab identity HTTP %d", resp.StatusCode)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogMetadataBytes+1))
			if err != nil {
				return "", err
			}
			if int64(len(body)) > maxCatalogMetadataBytes {
				return "", errors.New("GitLab identity response too large")
			}
			var value struct {
				PathWithNamespace string `json:"path_with_namespace"`
			}
			if err := json.Unmarshal(body, &value); err != nil {
				return "", err
			}
			return normalizeRepository(provider, value.PathWithNamespace)
		default:
			return "", errors.New("unsupported repository provider")
		}
	}
}
func githubSource(source string) (string, string, error) {
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("invalid Quiver GitHub source")
	}
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") {
		return "", "", errors.New("invalid Quiver GitHub source path")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." || strings.ContainsAny(strings.Join(parts, ""), "\\\x00\r\n") || !repositorySegmentPattern.MatchString(parts[0]) || !repositorySegmentPattern.MatchString(parts[1]) {
		return "", "", errors.New("invalid Quiver GitHub source")
	}
	return parts[0], parts[1], nil
}
func safeCatalogPath(name string) (string, error) {
	if strings.ContainsAny(name, "\\\x00\r\n?#") {
		return "", errors.New("unsafe catalog path")
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", errors.New("unsafe catalog path")
		}
	}
	return strings.Join(parts, "/"), nil
}

type registryIdentity struct{ provider, repo, id, release string }

func discoverQuiver(ctx context.Context, rd catalogReader, source, registryRoot string, changed bool, resolve IdentityResolver) (DiscoveryReport, error) {
	files := []string{"index.json", "platform-index.json", "community-app-catalog/Nintendo.json", "community-app-catalog/PlayStation.json", "community-app-catalog/Xbox.json", "community-app-catalog/OtherPlatforms.json"}
	data := make(map[string][]byte, len(files))
	for _, name := range files {
		b, err := rd.Read(ctx, name)
		if err != nil {
			return DiscoveryReport{}, fmt.Errorf("read %s: %w", name, err)
		}
		data[name] = b
	}
	_, platform, apps, err := decodeQuiverMetadata(data, files)
	if err != nil {
		return DiscoveryReport{}, err
	}
	cat, err := registry.ValidateTree(registryRoot)
	if err != nil {
		return DiscoveryReport{}, fmt.Errorf("validate registry: %w", err)
	}
	reg := registryIdentities(cat)
	selections := map[string][]quiverPlatformEntry{}
	for _, record := range platform.Entries {
		provider := normalizeProvider(record.Provider)
		repo, err := normalizeRepository(provider, record.Repository)
		if record.Provider != "" && err == nil {
			key := provider + ":" + repo
			selections[key] = append(selections[key], record)
		}
	}
	entries := make([]DiscoveryEntry, 0, len(apps))
	// Shared repositories must not amplify bounded input into an unbounded
	// report. Count repeated evidence before allocating its normalized arrays.
	remainingEvidence := maxCatalogMetadataBytes * 6
	// Exact catalog identities need no canonical lookup. Keep them visible to
	// the alias pass so an unmatched app does not resolve repositories that are
	// already represented exactly by another catalog application.
	exactCatalogRepos := make(map[string]bool, len(apps))
	for _, app := range apps {
		remainingEvidence -= 256 + int64(len(app.Name)+len(app.CatalogID)+len(app.FolderName)+len(app.Repository)+len(app.AppIconURL)+len(app.Project)+len(app.Group)+len(app.ReleaseAssetFilter))
		provider := normalizeProvider(app.RepositorySource)
		if strings.TrimSpace(app.RepositorySource) == "" {
			if parsed, parseErr := url.Parse(strings.TrimSpace(app.Repository)); parseErr == nil {
				if strings.EqualFold(parsed.Host, "github.com") {
					provider = "github"
				} else if strings.EqualFold(parsed.Host, "gitlab.com") {
					provider = "gitlab"
				}
			}
		}
		if repo, parseErr := normalizeRepository(provider, app.Repository); parseErr == nil {
			exactCatalogRepos[provider+":"+repo] = true
			for _, record := range selections[provider+":"+repo] {
				remainingEvidence -= int64(len(record.ReleaseTag))
				for _, asset := range record.AssetNames {
					remainingEvidence -= 2 * int64(len(asset))
				}
			}
		}
		if remainingEvidence < 0 {
			return DiscoveryReport{}, errors.New("discovery evidence exceeds size limit")
		}
	}
	resolverCache := map[string]string{}
	resolverErr := map[string]error{}
	resolverSeen := map[string]bool{}
	for _, a := range apps {
		provider := normalizeProvider(a.RepositorySource)
		if strings.TrimSpace(a.RepositorySource) == "" {
			if parsed, parseErr := url.Parse(strings.TrimSpace(a.Repository)); parseErr == nil {
				if strings.EqualFold(parsed.Host, "github.com") {
					provider = "github"
				} else if strings.EqualFold(parsed.Host, "gitlab.com") {
					provider = "gitlab"
				}
			}
		}
		repo, repoErr := normalizeRepository(provider, a.Repository)
		repositoryValue := repo
		if repoErr != nil {
			repositoryValue = strings.TrimSpace(a.Repository)
		}
		e := DiscoveryEntry{ExternalID: a.CatalogID, Name: a.Name, Category: a.Group, Project: a.Project, Provider: provider, Repository: repositoryValue, ReleaseAssetFilter: a.ReleaseAssetFilter, Architectures: []string{}, LinuxAssets: []string{}, Preferred: DiscoveryAssetGroup{Assets: []string{}}, Alternatives: []DiscoveryAssetGroup{}, RegistryMatches: []DiscoveryRegistryMatch{}}
		if e.ExternalID == "" {
			e.ExternalID = a.FolderName
		}
		if e.ExternalID == "" {
			e.ExternalID = e.Repository
		}
		if e.ExternalID == "" {
			e.ExternalID = "unknown"
		}
		e.ThumbnailURL = a.AppIconURL
		if e.Name == "" {
			e.Reasons = append(e.Reasons, "catalog application name is missing")
		}
		if repoErr != nil {
			e.Reasons = append(e.Reasons, "invalid repository identity")
		}
		matches := append([]quiverPlatformEntry{}, selections[provider+":"+repo]...)
		if len(matches) == 0 && repoErr == nil {
			e.Reasons = append(e.Reasons, "catalog has no platform selection")
		}
		if len(matches) > 1 {
			sort.Slice(matches, func(i, j int) bool {
				if matches[i].SelectionRevision != matches[j].SelectionRevision {
					return matches[i].SelectionRevision > matches[j].SelectionRevision
				}
				if matches[i].ReleaseTag != matches[j].ReleaseTag {
					return matches[i].ReleaseTag < matches[j].ReleaseTag
				}
				return assetNameKey(matches[i].AssetNames) < assetNameKey(matches[j].AssetNames)
			})
			for i := 1; i < len(matches); i++ {
				if matches[i].SelectionRevision != matches[0].SelectionRevision || matches[i].ReleaseTag != matches[0].ReleaseTag || !sameStrings(matches[i].AssetNames, matches[0].AssetNames) {
					e.Reasons = appendUnique(e.Reasons, "multiple platform selections")
				}
			}
		}
		if len(matches) > 0 {
			e.SelectionRevision = matches[0].SelectionRevision
		}
		filterMatched := false
		for _, p := range matches {
			for _, asset := range p.AssetNames {
				if strings.Contains(strings.ToLower(asset), strings.ToLower(a.ReleaseAssetFilter)) {
					filterMatched = true
				}
			}
			if p.AssetNames == nil {
				e.Reasons = appendUnique(e.Reasons, "catalog asset selection missing")
			}
			if p.SelectionRevision != 0 && p.SelectionRevision != 1 {
				e.Reasons = appendUnique(e.Reasons, "unknown catalog selection revision")
			}
			release := p.ReleaseTag
			if _, known := preferredReleaseTag(p.PreferredRelease); !known {
				e.Reasons = appendUnique(e.Reasons, "unknown preferred release metadata")
			}
			if release == "" {
				e.Reasons = appendUnique(e.Reasons, "catalog release metadata missing")
			}
			if release != "" {
				e.CatalogReleases = appendUnique(e.CatalogReleases, release)
			}
			classifyAssets(&e, p, a.ReleaseAssetFilter)
		}
		sort.Strings(e.CatalogReleases)
		if len(e.CatalogReleases) == 1 {
			e.CatalogRelease = e.CatalogReleases[0]
			e.CatalogReleases = nil
		} else if len(e.CatalogReleases) > 1 {
			e.Reasons = appendUnique(e.Reasons, "conflicting catalog release selection")
		}
		if a.ReleaseAssetFilter != "" && len(matches) > 0 && !filterMatched {
			e.Reasons = appendUnique(e.Reasons, "release asset filter selected no assets")
		}
		if repoErr == nil {
			e.RegistryMatches = registryMatches(reg, provider, repo)
		}
		if len(e.RegistryMatches) == 0 && repoErr == nil && resolve != nil {
			lookupIdentity := func(identity string) (string, error) {
				key := provider + ":" + identity
				if resolverSeen[key] {
					return resolverCache[key], resolverErr[key]
				}
				canonical, lookup := resolve(ctx, provider, identity)
				if lookup == nil {
					canonical, lookup = normalizeRepository(provider, canonical)
				}
				resolverSeen[key], resolverCache[key], resolverErr[key] = true, canonical, lookup
				return canonical, lookup
			}
			canonical, lookup := lookupIdentity(repo)
			unresolved := lookup != nil || canonical == ""
			if !unresolved && canonical != repo {
				matches := registryMatches(reg, provider, canonical)
				e.RegistryMatches = appendRegistryMatches(e.RegistryMatches, matches)
			}
			for _, registryRepo := range uniqueRegistryRepositories(reg, provider) {
				if registryRepo == repo || registryRepo == canonical || exactCatalogRepos[provider+":"+registryRepo] {
					continue
				}
				alias, aliasErr := lookupIdentity(registryRepo)
				if aliasErr != nil || alias == "" {
					unresolved = true
					continue
				}
				if alias == repo || (canonical != "" && alias == canonical) {
					matches := registryMatches(reg, provider, registryRepo)
					e.RegistryMatches = appendRegistryMatches(e.RegistryMatches, matches)
				}
			}
			if unresolved {
				e.Reasons = appendUnique(e.Reasons, "repository identity unresolved")
			}
		}
		finalizeAssetPreference(&e)
		for _, match := range e.RegistryMatches {
			remainingEvidence -= int64(len(match.ID) + len(match.Release))
		}
		if remainingEvidence < 0 {
			return DiscoveryReport{}, errors.New("discovery evidence exceeds size limit")
		}
		e.Status = classifyDiscoveryStatus(e)
		entries = append(entries, e)
	}
	// Repository identity does not establish the application-level association
	// when several catalog rows share a registry ID. Keep every match visible.
	matchUsers := map[string][]int{}
	identityUsers := map[string][]int{}
	for i, entry := range entries {
		identityUsers[entry.ExternalID] = append(identityUsers[entry.ExternalID], i)
		seen := map[string]bool{}
		for _, match := range entry.RegistryMatches {
			if !seen[match.ID] {
				matchUsers[match.ID] = append(matchUsers[match.ID], i)
				seen[match.ID] = true
			}
		}
	}
	for _, users := range matchUsers {
		if len(users) > 1 {
			for _, i := range users {
				entries[i].Reasons = appendUnique(entries[i].Reasons, "registry association shared by multiple catalog applications")
			}
		}
	}
	for _, users := range identityUsers {
		if len(users) > 1 {
			for _, i := range users {
				entries[i].Reasons = appendUnique(entries[i].Reasons, "duplicate external identity")
			}
		}
	}
	filtered := make([]DiscoveryEntry, 0, len(entries))
	for _, entry := range entries {
		sort.Strings(entry.Reasons)
		entry.Status = classifyDiscoveryStatus(entry)
		if !changed || isChangedStatus(entry.Status) {
			filtered = append(filtered, entry)
		}
	}
	entries = filtered
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ExternalID == entries[j].ExternalID {
			if entries[i].Repository == entries[j].Repository {
				return entrySortKey(entries[i]) < entrySortKey(entries[j])
			}
			return entries[i].Repository < entries[j].Repository
		}
		return entries[i].ExternalID < entries[j].ExternalID
	})
	report := DiscoveryReport{SchemaVersion: DiscoverySchemaVersion, Catalog: DiscoveryCatalog{Adapter: "quiver", Source: source, Revision: rd.Revision()}, Registry: DiscoveryRegistry{Source: registryRoot}, Changed: changed, Entries: entries}
	for _, e := range entries {
		switch e.Status {
		case "new":
			report.Summary.New++
		case "release-diff":
			report.Summary.ReleaseDiff++
		case "registered-same":
			report.Summary.RegisteredSame++
		case "unsupported":
			report.Summary.Unsupported++
		case "ambiguous":
			report.Summary.Ambiguous++
		case "no-linux":
			report.Summary.NoLinux++
		}
	}
	return report, nil
}

func assetNameKey(names []string) string {
	copyNames := append([]string{}, names...)
	sort.Strings(copyNames)
	return strings.Join(copyNames, "\x00")
}

func entrySortKey(e DiscoveryEntry) string {
	b, _ := json.Marshal(e)
	return string(b)
}
func finalizeAssetPreference(e *DiscoveryEntry) {
	if e.Preferred.Format != "" || len(e.Alternatives) == 0 {
		if e.Alternatives == nil {
			e.Alternatives = []DiscoveryAssetGroup{}
		}
		return
	}
	// The supported archive order is deterministic and presentation-only.
	e.Preferred = e.Alternatives[0]
	e.Alternatives = append([]DiscoveryAssetGroup{}, e.Alternatives[1:]...)
}
func normalizeProvider(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "github"
	}
	return v
}
func normalizeRepository(provider, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\\\x00\r\n?#") {
		return "", errors.New("empty or unsafe repository")
	}
	if provider != "github" && provider != "gitlab" {
		return "", errors.New("unsupported repository provider")
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
			return "", errors.New("unsafe repository URL")
		}
		hostProvider := ""
		if strings.EqualFold(u.Hostname(), "github.com") {
			hostProvider = "github"
		}
		if strings.EqualFold(u.Hostname(), "gitlab.com") {
			hostProvider = "gitlab"
		}
		if hostProvider == "" || hostProvider != provider {
			return "", errors.New("repository URL provider conflicts with declared provider")
		}
		if strings.Contains(u.EscapedPath(), "%2f") || strings.Contains(u.EscapedPath(), "%2F") {
			return "", errors.New("repository URL has escaped path separators")
		}
		raw = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	} else if err != nil || strings.Contains(raw, "://") {
		return "", errors.New("invalid repository identity")
	}
	raw = strings.TrimSuffix(strings.Trim(raw, "/"), ".git")
	parts := strings.Split(raw, "/")
	if (provider == "github" && len(parts) != 2) || (provider == "gitlab" && len(parts) < 2) {
		return "", errors.New("invalid repository path")
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !repositorySegmentPattern.MatchString(p) {
			return "", errors.New("invalid repository path")
		}
	}
	return strings.ToLower(strings.Join(parts, "/")), nil
}
func registryIdentities(cat *registry.Catalog) []registryIdentity {
	var out []registryIdentity
	for id, variants := range cat.Variants {
		for _, item := range variants {
			provider, repo := artifactRepository(item.Release.URL)
			if provider != "" && repo != "" {
				out = append(out, registryIdentity{provider: provider, repo: repo, id: id, release: item.Release.Version})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].provider != out[j].provider {
			return out[i].provider < out[j].provider
		}
		if out[i].repo != out[j].repo {
			return out[i].repo < out[j].repo
		}
		if out[i].id != out[j].id {
			return out[i].id < out[j].id
		}
		return out[i].release < out[j].release
	})
	return out
}
func registryMatches(all []registryIdentity, provider, repo string) []DiscoveryRegistryMatch {
	seen := map[string]bool{}
	out := []DiscoveryRegistryMatch{}
	for _, x := range all {
		key := x.id + "\x00" + x.release
		if x.provider == provider && x.repo == repo && !seen[key] {
			seen[key] = true
			out = append(out, DiscoveryRegistryMatch{ID: x.id, Release: x.release})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Release < out[j].Release
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func appendRegistryMatches(dst, src []DiscoveryRegistryMatch) []DiscoveryRegistryMatch {
	if dst == nil {
		dst = []DiscoveryRegistryMatch{}
	}
	seen := make(map[string]bool, len(dst)+len(src))
	for _, m := range dst {
		seen[m.ID+"\x00"+m.Release] = true
	}
	for _, m := range src {
		key := m.ID + "\x00" + m.Release
		if !seen[key] {
			dst = append(dst, m)
			seen[key] = true
		}
	}
	sort.Slice(dst, func(i, j int) bool {
		if dst[i].ID == dst[j].ID {
			return dst[i].Release < dst[j].Release
		}
		return dst[i].ID < dst[j].ID
	})
	return dst
}
func uniqueRegistryRepositories(all []registryIdentity, provider string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range all {
		if item.provider == provider && !seen[item.repo] {
			seen[item.repo] = true
			out = append(out, item.repo)
		}
	}
	sort.Strings(out)
	return out
}
func artifactRepository(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", ""
	}
	host := strings.ToLower(u.Hostname())
	provider := ""
	if host == "github.com" {
		provider = "github"
	}
	if host == "gitlab.com" {
		provider = "gitlab"
	}
	if provider == "" {
		return "", ""
	}
	p := strings.Trim(u.Path, "/")
	if provider == "github" {
		parts := strings.Split(p, "/")
		if len(parts) < 5 {
			return "", ""
		}
		p = strings.Join(parts[:2], "/")
	} else {
		for _, marker := range []string{"/-/releases", "/-/package", "/-/archive/", "/releases/"} {
			if i := strings.Index(p, marker); i >= 0 {
				p = p[:i]
				break
			}
		}
	}
	repo, err := normalizeRepository(provider, p)
	if err != nil {
		return "", ""
	}
	return provider, repo
}

func classifyAssets(e *DiscoveryEntry, p quiverPlatformEntry, releaseFilter string) {
	for _, name := range p.AssetNames {
		low := strings.ToLower(name)
		if releaseFilter != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(releaseFilter)) {
			continue
		}
		format := artifactFormat(name)
		if format == "" && strings.HasSuffix(low, ".deb") {
			format = "deb"
		}
		filenameLinux := format == "appimage" || strings.HasSuffix(low, ".deb") || linuxAssetPattern.MatchString(name) || InferPlatform(name) != ""
		if !filenameLinux {
			continue
		}
		if isClearlyNonLinux(low) {
			e.Reasons = appendUnique(e.Reasons, "conflicting platform markers: "+name)
			continue
		}
		if !contains(e.LinuxAssets, name) {
			e.LinuxAssets = append(e.LinuxAssets, name)
		}
		architectures := assetArchitectures(name)
		for _, arch := range architectures {
			if !contains(e.Architectures, arch) {
				e.Architectures = append(e.Architectures, arch)
			}
		}
		eligibleArchitecture := false
		unsupportedArchitecture := false
		for _, arch := range architectures {
			if arch == "unknown" || supportedLinuxArchitecture(arch) {
				eligibleArchitecture = true
			} else {
				unsupportedArchitecture = true
			}
		}
		if eligibleArchitecture && unsupportedArchitecture {
			e.Reasons = appendUnique(e.Reasons, "conflicting architecture markers: "+name)
			continue
		}
		if format == "" || !eligibleArchitecture {
			continue
		}
		if format == "appimage" {
			e.Preferred.Assets = appendUnique(e.Preferred.Assets, name)
			e.Preferred.Format = "appimage"
		} else if format == "tar.gz" || format == "tar.xz" || format == "zip" {
			addAssetGroup(&e.Alternatives, format, name)
		}
	}
	sort.Strings(e.LinuxAssets)
	sort.Strings(e.Architectures)
	sort.Strings(e.Preferred.Assets)
	sortAssetGroups(e.Alternatives)
}

func supportedLinuxArchitecture(arch string) bool {
	_, ok := manifest.ParsePlatformKey("linux-" + arch)
	return ok
}
func isClearlyNonLinux(name string) bool {
	if nonLinuxAssetPattern.MatchString(name) {
		return true
	}
	for _, x := range []string{".dmg", ".msi", ".exe", ".apk", ".ipa"} {
		if strings.HasSuffix(name, x) {
			return true
		}
	}
	return false
}
func assetArchitectures(name string) []string {
	architectures := []string{}
	if arch := TargetArchitecture(name); arch != "" {
		architectures = appendUnique(architectures, arch)
	}
	if amd64AssetPattern.MatchString(name) {
		architectures = appendUnique(architectures, "amd64")
	}
	if arm64AssetPattern.MatchString(name) {
		architectures = appendUnique(architectures, "arm64")
	}
	if armhfAssetPattern.MatchString(name) {
		architectures = append(architectures, "armhf")
	}
	if i386AssetPattern.MatchString(name) || (x86AssetPattern.MatchString(name) && !amd64AssetPattern.MatchString(name)) {
		architectures = append(architectures, "i386")
	}
	for _, match := range unsupportedAssetArchitecturePattern.FindAllStringSubmatch(name, -1) {
		architectures = appendUnique(architectures, strings.ToLower(match[2]))
	}
	if len(architectures) == 0 {
		architectures = append(architectures, "unknown")
	}
	sort.Strings(architectures)
	return architectures
}
func addAssetGroup(groups *[]DiscoveryAssetGroup, format, asset string) {
	for i := range *groups {
		if (*groups)[i].Format == format {
			(*groups)[i].Assets = appendUnique((*groups)[i].Assets, asset)
			return
		}
	}
	*groups = append(*groups, DiscoveryAssetGroup{Format: format, Assets: []string{asset}})
}
func appendUnique(v []string, s string) []string {
	if !contains(v, s) {
		return append(v, s)
	}
	return v
}
func sortAssetGroups(v []DiscoveryAssetGroup) {
	order := map[string]int{"tar.gz": 0, "tar.xz": 1, "zip": 2, "deb": 3}
	for i := range v {
		sort.Strings(v[i].Assets)
	}
	sort.Slice(v, func(i, j int) bool {
		if order[v[i].Format] != order[v[j].Format] {
			return order[v[i].Format] < order[v[j].Format]
		}
		return v[i].Format < v[j].Format
	})
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
func isChangedStatus(s string) bool {
	return s == "new" || s == "release-diff" || s == "unsupported" || s == "ambiguous"
}
func classifyDiscoveryStatus(e DiscoveryEntry) string {
	if len(e.Reasons) > 0 {
		return "ambiguous"
	}
	if len(e.LinuxAssets) == 0 {
		return "no-linux"
	}
	if len(e.Preferred.Assets) == 0 && len(e.Alternatives) == 0 {
		return "unsupported"
	}
	if e.CatalogRelease == "" {
		return "ambiguous"
	}
	if len(e.RegistryMatches) == 0 {
		return "new"
	}
	for _, m := range e.RegistryMatches {
		if m.Release == "" {
			return "ambiguous"
		}
	}
	for _, m := range e.RegistryMatches {
		if m.Release != e.CatalogRelease {
			return "release-diff"
		}
	}
	return "registered-same"
}

func RenderDiscovery(w io.Writer, r DiscoveryReport, markdown bool) error {
	if markdown {
		if _, err := fmt.Fprintf(w, "# Catalog discovery\n\nCatalog: %s %s  \nRegistry: %s  \nView: %s\n\nSummary: new %d, release-diff %d, registered-same %d, unsupported %d, ambiguous %d, no-linux %d\n\n| App | Catalog release | Registry | Arch | Preferred | Alternatives | Status |\n| --- | --- | --- | --- | --- | --- | --- |\n", markdownText(r.Catalog.Source), revisionText(r.Catalog.Revision), markdownText(r.Registry.Source), viewText(r.Changed), r.Summary.New, r.Summary.ReleaseDiff, r.Summary.RegisteredSame, r.Summary.Unsupported, r.Summary.Ambiguous, r.Summary.NoLinux); err != nil {
			return err
		}
		for _, e := range r.Entries {
			if _, err := fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s |\n", markdownText(appText(e)), markdownText(e.CatalogRelease), markdownText(matches(e.RegistryMatches)), markdownText(strings.Join(e.Architectures, ", ")), markdownText(groupText(e.Preferred)), markdownText(groups(e.Alternatives)), markdownText(statusText(e))); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	revision := r.Catalog.Revision
	if revision == "" {
		revision = "local"
	}
	if _, err := fmt.Fprintf(tw, "Catalog: %s (%s)\nRegistry: %s\nView: %s\nSummary: new %d, release-diff %d, registered-same %d, unsupported %d, ambiguous %d, no-linux %d\n\n", safe(r.Catalog.Source), safe(revision), safe(r.Registry.Source), safe(viewText(r.Changed)), r.Summary.New, r.Summary.ReleaseDiff, r.Summary.RegisteredSame, r.Summary.Unsupported, r.Summary.Ambiguous, r.Summary.NoLinux); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(tw, "APP\tCATALOG RELEASE\tREGISTRY\tARCH\tPREFERRED\tALTERNATIVES\tSTATUS"); err != nil {
		return err
	}
	for _, e := range r.Entries {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", safe(appText(e)), safe(e.CatalogRelease), safe(matches(e.RegistryMatches)), safe(strings.Join(e.Architectures, ",")), safe(compactGroup(e.Preferred)), safe(compactGroups(e.Alternatives)), safe(statusText(e))); err != nil {
			return err
		}
	}
	return tw.Flush()
}
func viewText(changed bool) string {
	if changed {
		return "changed (summary and entries are filtered)"
	}
	return "all evaluated entries"
}
func appText(e DiscoveryEntry) string {
	name := e.Name
	if name == "" {
		name = "(unnamed)"
	}
	if e.Status != "ambiguous" {
		return name
	}
	identity := e.Provider + "/" + e.Repository
	if e.ExternalID != "" {
		identity = e.ExternalID + "; " + identity
	}
	return name + " [" + identity + "]"
}
func statusText(e DiscoveryEntry) string {
	if len(e.Reasons) == 0 {
		return e.Status
	}
	return e.Status + " (" + strings.Join(e.Reasons, "; ") + ")"
}
func revisionText(v string) string {
	if v == "" {
		return "revision: local"
	}
	return "revision: `" + markdownText(v) + "`"
}
func groupText(g DiscoveryAssetGroup) string {
	if g.Format == "" {
		return "unknown"
	}
	return g.Format + ":" + strings.Join(g.Assets, ",")
}
func compactGroup(g DiscoveryAssetGroup) string {
	if g.Format == "" {
		return "unknown"
	}
	return fmt.Sprintf("%s (%d)", g.Format, len(g.Assets))
}
func compactGroups(v []DiscoveryAssetGroup) string {
	out := make([]string, 0, len(v))
	for _, g := range v {
		out = append(out, compactGroup(g))
	}
	return strings.Join(out, "; ")
}
func matches(v []DiscoveryRegistryMatch) string {
	var out []string
	for _, m := range v {
		rel := m.Release
		if rel == "" {
			rel = "?"
		}
		out = append(out, m.ID+"@"+rel)
	}
	return strings.Join(out, ", ")
}
func groups(v []DiscoveryAssetGroup) string {
	var out []string
	for _, g := range v {
		out = append(out, groupText(g))
	}
	return strings.Join(out, "; ")
}
func safe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func markdownText(s string) string {
	s = safe(s)
	s = strings.ReplaceAll(s, "\\", "\\\\")
	// Escaping the colon keeps untrusted https://text from becoming a GFM
	// autolink while rendering the visible value unchanged.
	s = strings.ReplaceAll(s, ":", "\\:")
	s = strings.ReplaceAll(s, "<", "\\<")
	s = strings.ReplaceAll(s, ">", "\\>")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "]", "\\]")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "_", "\\_")
	return strings.ReplaceAll(s, "\n", " ")
}

// ValidateDiscoveryReport checks the schema contract without a schema package.
// It is used before reports are emitted or paired on disk.
func ValidateDiscoveryReport(report DiscoveryReport) error {
	if report.SchemaVersion != DiscoverySchemaVersion {
		return fmt.Errorf("unsupported discovery schema version %d", report.SchemaVersion)
	}
	if report.Catalog.Adapter != "quiver" || report.Catalog.Source == "" {
		return errors.New("discovery catalog provenance is invalid")
	}
	if strings.HasPrefix(report.Catalog.Source, "https://github.com/") && (len(report.Catalog.Revision) != 40 || strings.Trim(report.Catalog.Revision, "0123456789abcdefABCDEF") != "") {
		return errors.New("remote discovery catalog revision is not an immutable SHA")
	}
	if !strings.HasPrefix(report.Catalog.Source, "https://github.com/") && report.Catalog.Revision != "" {
		return errors.New("local discovery catalog cannot claim an immutable revision")
	}
	if report.Registry.Source == "" {
		return errors.New("discovery registry source is empty")
	}
	if report.Entries == nil {
		return errors.New("discovery entries array is null")
	}
	counts := DiscoverySummary{}
	validStatus := map[string]bool{"new": true, "release-diff": true, "registered-same": true, "unsupported": true, "ambiguous": true, "no-linux": true}
	for i, entry := range report.Entries {
		if report.Changed && !isChangedStatus(entry.Status) {
			return fmt.Errorf("entry %d is outside the changed view", i)
		}
		if entry.ExternalID == "" || entry.Provider == "" || entry.Status == "" || !validStatus[entry.Status] {
			return fmt.Errorf("entry %d has invalid identity or status", i)
		}
		if classifyDiscoveryStatus(entry) != entry.Status {
			return fmt.Errorf("entry %d status is inconsistent with its evidence", i)
		}
		if entry.Architectures == nil || entry.LinuxAssets == nil || entry.Alternatives == nil || entry.RegistryMatches == nil || entry.Preferred.Assets == nil {
			return fmt.Errorf("entry %d contains a null array", i)
		}
		for _, group := range entry.Alternatives {
			if group.Format == "" || group.Assets == nil {
				return fmt.Errorf("entry %d has invalid alternative group", i)
			}
		}
		for _, match := range entry.RegistryMatches {
			if match.ID == "" {
				return fmt.Errorf("entry %d has an invalid registry match", i)
			}
		}
		switch entry.Status {
		case "new":
			counts.New++
		case "release-diff":
			counts.ReleaseDiff++
		case "registered-same":
			counts.RegisteredSame++
		case "unsupported":
			counts.Unsupported++
		case "ambiguous":
			counts.Ambiguous++
		case "no-linux":
			counts.NoLinux++
		}
	}
	if counts != report.Summary {
		return fmt.Errorf("discovery summary does not match entries")
	}
	return nil
}

// WriteDiscoveryReports creates the paired report files from one result. It
// refuses an existing target so a failed second write cannot destroy a prior
// complete pair; callers should use a fresh workflow artifact directory.
func WriteDiscoveryReports(dir string, report DiscoveryReport) error {
	if err := ValidateDiscoveryReport(report); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("report output directory must be a real directory")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, name := range []string{"discovery.json", "discovery.md"} {
		if info, statErr := os.Lstat(filepath.Join(dir, name)); statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return fmt.Errorf("report target %s is unsafe", name)
			}
			return fmt.Errorf("report target %s already exists", name)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	jsonBytes = append(jsonBytes, '\n')
	var md strings.Builder
	if err := RenderDiscovery(&md, report, true); err != nil {
		return err
	}
	type tempFile struct{ name, target string }
	temps := make([]tempFile, 0, 2)
	cleanup := func() {
		for _, f := range temps {
			_ = os.Remove(f.name)
		}
	}
	defer cleanup()
	for _, f := range []struct {
		name string
		data []byte
	}{{"discovery.json", jsonBytes}, {"discovery.md", []byte(md.String())}} {
		tmp, err := os.CreateTemp(dir, ".discovery-")
		if err != nil {
			return err
		}
		name := tmp.Name()
		temps = append(temps, tempFile{name: name, target: filepath.Join(dir, f.name)})
		if _, err := tmp.Write(f.data); err != nil {
			_ = tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
	}
	if err := os.Rename(temps[0].name, temps[0].target); err != nil {
		return err
	}
	if err := os.Rename(temps[1].name, temps[1].target); err != nil {
		_ = os.Remove(temps[0].target)
		return err
	}
	return nil
}
