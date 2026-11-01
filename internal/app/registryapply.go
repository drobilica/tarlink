package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
	"github.com/drobilica/tarlink/internal/research"
	"go.yaml.in/yaml/v3"
)

// RegistryApplyService prepares verified release additions in a local checkout.
// It never installs applications, publishes metadata, or changes Git state.
type RegistryApplyService interface {
	ApplyRegistryUpdates(context.Context, RegistryApplyOptions) (RegistryApplyReport, error)
}
type RegistryApplyOptions struct {
	Root   string
	DryRun bool
}
type RegistryApplyReport struct {
	SchemaVersion int                   `json:"schema_version"`
	DryRun        bool                  `json:"dry_run"`
	Results       []RegistryApplyResult `json:"results"`
}
type RegistryApplyResult struct {
	App      string              `json:"app"`
	Status   string              `json:"status"`
	Reason   string              `json:"reason,omitempty"`
	Versions []string            `json:"versions,omitempty"`
	Edit     *RegistryEditReport `json:"edit,omitempty"`
}

func (m *Maintainer) ApplyRegistryUpdates(ctx context.Context, options RegistryApplyOptions) (RegistryApplyReport, error) {
	report := RegistryApplyReport{SchemaVersion: 1, DryRun: options.DryRun, Results: []RegistryApplyResult{}}
	if options.Root == "" {
		return report, errors.New("local registry path is required")
	}
	root, err := filepath.Abs(options.Root)
	if err != nil {
		return report, err
	}
	if err := filesystem.CheckOwnedDirectoryWithin(filepath.VolumeName(root)+string(filepath.Separator), root); err != nil {
		return report, err
	}
	catalog, err := registry.ValidateTree(root)
	if err != nil {
		return report, err
	}
	cache, err := os.MkdirTemp("", "tarlink-registry-update-*")
	if err != nil {
		return report, err
	}
	defer os.RemoveAll(cache)
	httpClient := download.NewClient().HTTP
	if m.client != nil && m.client.HTTP != nil {
		copy := *m.client.HTTP
		httpClient = &copy
	}
	if httpClient.Timeout <= 0 || httpClient.Timeout > 30*time.Second {
		httpClient.Timeout = 30 * time.Second
	}
	client := &research.Client{Refresh: true, CacheRoot: cache, HTTP: httpClient}
	ids := make([]string, 0, len(catalog.Variants))
	for id := range catalog.Variants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return report, errors.Join(append(failures, err)...)
		}
		result := RegistryApplyResult{App: id, Status: "no-change"}
		repository, approved, err := freshness.Repository(id)
		if err != nil {
			return report, err
		}
		if !approved {
			result.Status, result.Reason = "skipped", "no_approved_upstream"
			report.Results = append(report.Results, result)
			continue
		}
		previous, err := readRegistryManifest(filepath.Join(root, "apps", id, "manifest.yaml"))
		var updated []byte
		if err == nil {
			var document *manifest.Document
			document, err = manifest.ParseBytes(previous)
			if err == nil && document.Enabled != nil && !*document.Enabled {
				result.Status, result.Reason = "skipped", "disabled"
			} else if err == nil {
				updated, result.Versions, err = prepareRegistryUpdate(ctx, client, repository, previous, document)
			}
		}
		if err == nil && len(updated) > 0 {
			var edit RegistryEditReport
			edit, err = m.EditRegistry(ctx, RegistryEditOptions{Root: root, ID: id, Update: updated, Expected: previous, DryRun: options.DryRun})
			result.Edit = &edit
			if edit.Written {
				result.Status = "updated"
			} else if err == nil {
				result.Status = "would-update"
			}
		}
		if err != nil {
			result.Status, result.Reason = "error", err.Error()
			failures = append(failures, fmt.Errorf("%s: %w", id, err))
		}
		report.Results = append(report.Results, result)
	}
	return report, errors.Join(failures...)
}

func prepareRegistryUpdate(ctx context.Context, client *research.Client, repository string, previous []byte, document *manifest.Document) ([]byte, []string, error) {
	releases, err := client.Discover(ctx, repository)
	if err != nil {
		return nil, nil, err
	}
	sort.SliceStable(releases, func(i, j int) bool { return releases[i].PublishedAt.After(releases[j].PublishedAt) })
	channels := map[string]manifest.ChannelHead{"stable": {Current: document.Release.Current}}
	if document.Release.Current == "" {
		channels = document.Release.Channels
	}
	names := make([]string, 0, len(channels))
	for channel := range channels {
		names = append(names, channel)
	}
	sort.Strings(names)
	originalReleases := len(document.Release.Releases)
	var versions []string
	for _, channel := range names {
		var latest *research.Release
		for i := range releases {
			release := &releases[i]
			if release.Draft || (channel == "stable") == release.Prerelease {
				continue
			}
			latest = release
			break
		}
		if latest == nil {
			continue
		}
		if latest.PublishedAt.IsZero() {
			return nil, nil, errors.New("release publication time missing; manual review required")
		}
		version := latest.Tag
		if len(version) > 1 && version[0] == 'v' && version[1] >= '0' && version[1] <= '9' {
			version = version[1:]
		}
		// Test membership before selecting any missing version: do not backfill history.
		exists := false
		var source manifest.ReleaseDefinition
		for _, release := range document.Release.Releases {
			releaseChannel := release.Channel
			if releaseChannel == "" {
				releaseChannel = "stable"
			}
			if releaseChannel == channel && (release.Version == version || release.Version == latest.Tag) {
				exists = true
			}
			if releaseChannel == channel && release.Version == channels[channel].Current {
				source = release
			}
		}
		if exists {
			continue
		}
		var currentPublished time.Time
		for _, release := range releases {
			tag := release.Tag
			if len(tag) > 1 && tag[0] == 'v' && tag[1] >= '0' && tag[1] <= '9' {
				tag = tag[1:]
			}
			if !release.Draft && (channel == "stable") != release.Prerelease && (tag == channels[channel].Current || release.Tag == channels[channel].Current) {
				currentPublished = release.PublishedAt
				break
			}
		}
		newer, err := freshness.IsNewerRelease(version, channels[channel].Current, latest.PublishedAt, currentPublished)
		if err != nil {
			return nil, nil, err
		}
		if !newer {
			continue
		}
		if source.Version == "" {
			return nil, nil, fmt.Errorf("%s: current release unavailable", channel)
		}
		if source.NestedArchive != nil {
			return nil, nil, errors.New("nested archives require manual review")
		}
		addition := source
		addition.Version = version
		addition.Artifacts = make(map[string]manifest.Artifact, len(source.Artifacts))
		platforms := make([]string, 0, len(source.Artifacts))
		for platform := range source.Artifacts {
			platforms = append(platforms, platform)
		}
		sort.Strings(platforms)
		for _, platform := range platforms {
			old := source.Artifacts[platform]
			format := old.Archive
			if format == "" {
				format = document.Release.Archive
			}
			var assets []research.Asset
			for _, asset := range latest.Assets {
				if research.InferPlatform(asset.Name) == platform && registryAssetFormat(asset.Name) == format {
					assets = append(assets, asset)
				}
			}
			if len(assets) != 1 {
				return nil, nil, fmt.Errorf("%s/%s: expected one %s asset, found %d; manual review required", channel, platform, format, len(assets))
			}
			asset := assets[0]
			// Only exact assets under the reviewed upstream repository are eligible.
			target, err := research.ParseReleaseAssetURL(asset.URL)
			if err != nil || string(target.Repository) != repository || target.Tag != latest.Tag || target.Asset != asset.Name {
				return nil, nil, errors.New("asset does not belong to the approved repository release")
			}
			provenance := research.EvaluateProvenance(latest.Repository, *latest, asset)
			var inspection research.Inspection
			if provenance.Verdict == research.Acceptable {
				inspection, err = client.InspectAsset(ctx, asset, provenance, archive.Format(format), research.TargetArchitecture(asset.Name))
			} else if provenance.ReasonCode == "NO_AUTHORITATIVE_DIGEST" {
				artifact, fetchErr := client.FetchUnverified(ctx, asset)
				if fetchErr != nil {
					return nil, nil, fetchErr
				}
				inspection, err = research.Inspect(ctx, artifact, archive.Format(format), research.TargetArchitecture(asset.Name))
				_ = artifact.Cleanup()
			} else {
				return nil, nil, errors.New(provenance.Message)
			}
			if err != nil {
				return nil, nil, err
			}
			if len(inspection.Blockers) != 0 || inspection.ArtifactType != format {
				return nil, nil, fmt.Errorf("artifact requires manual review: %v", inspection.Blockers)
			}
			current := document.Platforms[platform]
			application := current.Application
			for _, release := range current.ReleaseHistory.Releases {
				if release.Version == source.Version && release.Channel == channel {
					if release.Application != nil {
						application = *release.Application
					}
					break
				}
			}
			for _, executable := range application.Executables {
				if !(format == "appimage" && executable.Path == "appimage") && !contains(inspection.Executables, executable.Path) {
					return nil, nil, fmt.Errorf("executable %q changed; manual review required", executable.Path)
				}
			}
			if current.Desktop.Icon.Path != "" && !contains(inspection.Icons, current.Desktop.Icon.Path) {
				return nil, nil, errors.New("desktop icon changed; manual review required")
			}
			digest := inspection.ComputedDigests[document.Release.Verification.Algorithm]
			if digest == "" {
				return nil, nil, errors.New("artifact inspection did not compute the registry verification digest")
			}
			old.URL, old.Verification = asset.URL, manifest.ArtifactVerification{Digest: digest, Source: asset.URL}
			addition.Artifacts[platform] = old
		}
		document.Release.Releases = append(document.Release.Releases, addition)
		if document.Release.Current != "" {
			document.Release.Current = version
		} else {
			document.Release.Channels[channel] = manifest.ChannelHead{Current: version}
		}
		versions = append(versions, channel+":"+version)
	}
	if len(versions) == 0 {
		return nil, nil, nil
	}
	// Preserve existing history nodes and comments, changing only channel heads
	// and appending the new releases.
	var node yaml.Node
	if err := yaml.Unmarshal(previous, &node); err != nil {
		return nil, nil, err
	}
	mappingValue := func(mapping *yaml.Node, key string) *yaml.Node {
		for i := 0; i < len(mapping.Content); i += 2 {
			if mapping.Content[i].Value == key {
				return mapping.Content[i+1]
			}
		}
		return nil
	}
	releaseNode := mappingValue(node.Content[0], "release")
	if current := mappingValue(releaseNode, "current"); current != nil {
		current.Value = document.Release.Current
	} else {
		channelsNode := mappingValue(releaseNode, "channels")
		for channel, head := range document.Release.Channels {
			mappingValue(mappingValue(channelsNode, channel), "current").Value = head.Current
		}
	}
	releasesNode := mappingValue(releaseNode, "releases")
	for _, release := range document.Release.Releases[originalReleases:] {
		var addition yaml.Node
		if err := addition.Encode(release); err != nil {
			return nil, nil, err
		}
		releasesNode.Content = append(releasesNode.Content, &addition)
	}
	data, err := yaml.Marshal(&node)
	if err != nil {
		return nil, nil, err
	}
	if _, err := manifest.ParseBytes(data); err != nil {
		return nil, nil, err
	}
	return data, versions, nil
}

func registryAssetFormat(name string) string {
	name = strings.ToLower(name)
	for _, suffix := range []struct{ suffix, format string }{{".appimage", "appimage"}, {".tar.gz", "tar.gz"}, {".tgz", "tar.gz"}, {".tar.xz", "tar.xz"}, {".zip", "zip"}} {
		if strings.HasSuffix(name, suffix.suffix) {
			return suffix.format
		}
	}
	return ""
}
