// Package manifest implements TarLink's deliberately small, declarative v5
// application manifest. A manifest can describe data only; it cannot request
// process execution, hooks, arbitrary destinations, or command arguments.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/drobilica/tarlink/internal/checksum"
	"go.yaml.in/yaml/v3"
)

const (
	SchemaV1         = 1 // retired; retained as a name for callers describing old data.
	SchemaV2         = 2
	SchemaV3         = 3 // retired
	SchemaV5         = 5
	CurrentSchema    = SchemaV5
	MaxManifestBytes = 1 << 20
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Manifest struct {
	Schema       int      `json:"schema"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Summary      string   `json:"summary"`
	Homepage     string   `json:"homepage"`
	Categories   []string `json:"categories"`
	Requirements []string `json:"requirements,omitempty"`
	Platform     Platform `json:"platform"`
	// Release is the selected default-channel head. It is populated from
	// ReleaseHistory by Parse and by registry resolution; it is not itself a
	// YAML field. Keeping this convenience view avoids making consumers parse
	// registry history when they only need the artifact to install.
	Release        Release        `json:"release"`
	ReleaseHistory ReleaseHistory `json:"release_history"`
	Application    Application    `json:"application"`
	Desktop        Desktop        `json:"desktop"`
}

// ResolvedPackage is the exact single-platform package selected from a
// Document. Its embedded Manifest is intentionally the same shape consumed by
// the install lifecycle; Fingerprint identifies the effective package inputs,
// not the source YAML representation or the unselected release history.
//
// Construct values with Document.ResolvePackage. The type is exported so
// callers that need an auditable resolution boundary do not have to duplicate
// the platform/channel selection logic.
type ResolvedPackage struct {
	Manifest
	Fingerprint string `json:"fingerprint"`
}

type Platform struct {
	OS   string `yaml:"os" json:"os"`
	Arch string `yaml:"arch" json:"arch"`
}

// Document is the one-file registry representation for an application.
type Document struct {
	Schema       int                   `yaml:"schema" json:"schema"`
	ID           string                `yaml:"id" json:"id"`
	Name         string                `yaml:"name" json:"name"`
	Summary      string                `yaml:"summary" json:"summary"`
	Homepage     string                `yaml:"homepage" json:"homepage"`
	Categories   []string              `yaml:"categories" json:"categories"`
	Requirements []string              `yaml:"requirements,omitempty" json:"requirements,omitempty"`
	Release      ReleaseDocument       `yaml:"release" json:"release"`
	Application  ApplicationDefinition `yaml:"application" json:"application"`
	Desktop      *DesktopDefinition    `yaml:"desktop,omitempty" json:"desktop,omitempty"`
	// Platforms is derived from release.artifacts and is never decoded from YAML.
	Platforms map[string]PlatformManifest `yaml:"-" json:"-"`
}

// ReleaseDocument is the v5 release history. Single-channel manifests use
// Current and omit DefaultChannel/Channels and per-entry Channel. Multi-channel
// manifests use the expanded channel form.
type ReleaseDocument struct {
	Current        string                 `yaml:"current,omitempty" json:"current,omitempty"`
	DefaultChannel string                 `yaml:"default-channel,omitempty" json:"default_channel,omitempty"`
	Channels       map[string]ChannelHead `yaml:"channels,omitempty" json:"channels,omitempty"`
	Archive        string                 `yaml:"archive,omitempty" json:"archive,omitempty"`
	Verification   ReleaseVerification    `yaml:"verification" json:"verification"`
	Releases       []ReleaseDefinition    `yaml:"releases" json:"releases"`
}

type ReleaseVerification struct {
	Algorithm string `yaml:"algorithm" json:"algorithm"`
}

// ReleaseDefinition is one retained release. Platform and artifact identity
// comes only from Artifacts; there is no top-level platforms declaration.
type ReleaseDefinition struct {
	Channel       string              `yaml:"channel" json:"channel"`
	Version       string              `yaml:"version" json:"version"`
	Artifacts     map[string]Artifact `yaml:"artifacts" json:"artifacts"`
	NestedArchive *NestedArchive      `yaml:"nested-archive,omitempty" json:"nested_archive,omitempty"`
}

// Artifact is the authoritative package input for one canonical platform.
// Archive and verification may be inherited only from the containing release
// when that release uses the shared form; per-artifact overrides are rejected.
type Artifact struct {
	URL          string               `yaml:"url" json:"url"`
	Archive      string               `yaml:"archive,omitempty" json:"archive,omitempty"`
	Verification ArtifactVerification `yaml:"verification" json:"verification"`
}

type ArtifactVerification struct {
	Digest string `yaml:"digest" json:"digest"`
	Source string `yaml:"source" json:"source"`
}

type ApplicationDefinition struct {
	Executable  *ExecutableDefinition  `yaml:"executable,omitempty" json:"executable,omitempty"`
	Executables []ExecutableDefinition `yaml:"executables,omitempty" json:"executables,omitempty"`
}

type ExecutableDefinition struct {
	Name          string            `yaml:"name,omitempty" json:"name,omitempty"`
	Path          string            `yaml:"path,omitempty" json:"path,omitempty"`
	Paths         map[string]string `yaml:"paths,omitempty" json:"paths,omitempty"`
	CreateBinLink *bool             `yaml:"create-bin-link,omitempty" json:"create_bin_link,omitempty"`
}

type DesktopDefinition struct {
	Executable       string       `yaml:"executable,omitempty" json:"executable,omitempty"`
	WorkingDirectory string       `yaml:"working-directory,omitempty" json:"working_directory,omitempty"`
	Categories       []string     `yaml:"categories" json:"categories"`
	Icon             *DesktopIcon `yaml:"icon,omitempty" json:"icon,omitempty"`
}

const (
	PlatformLinuxAMD64 = "linux-amd64"
	PlatformLinuxARM64 = "linux-arm64"
)

func PlatformKey(p Platform) string { return p.OS + "-" + p.Arch }

func ParsePlatformKey(key string) (Platform, bool) {
	switch key {
	case PlatformLinuxAMD64:
		return Platform{OS: "linux", Arch: "amd64"}, true
	case PlatformLinuxARM64:
		return Platform{OS: "linux", Arch: "arm64"}, true
	default:
		return Platform{}, false
	}
}

// ResolvePackage validates and projects one exact platform definition into
// the logical package consumed by the install lifecycle.
func (d *Document) ResolvePackage(key string) (*ResolvedPackage, error) {
	if d == nil {
		return nil, errors.New("manifest document is nil")
	}
	if err := d.validateShared(); err != nil {
		return nil, err
	}
	platforms, err := d.derivedPlatforms()
	if err != nil {
		return nil, err
	}
	for platformKey := range platforms {
		resolved, err := d.resolvePlatform(platforms, platformKey)
		if err != nil {
			return nil, fmt.Errorf("platform %s: %w", platformKey, err)
		}
		if err := resolved.Validate(); err != nil {
			return nil, fmt.Errorf("platform %s: %w", platformKey, err)
		}
		if err := resolved.ValidateHistory(); err != nil {
			return nil, fmt.Errorf("platform %s: %w", platformKey, err)
		}
	}
	resolved, err := d.resolvePlatform(platforms, key)
	if err != nil {
		return nil, err
	}
	fingerprint, err := resolved.ResolvedPackageFingerprint()
	if err != nil {
		return nil, err
	}
	return &ResolvedPackage{Manifest: resolved, Fingerprint: fingerprint}, nil
}

// ResolvePlatform preserves the lifecycle-facing resolved-package API while using the
// strict schema-v5 resolution boundary and copy semantics.
func (d *Document) ResolvePlatform(key string) (*Manifest, error) {
	resolved, err := d.ResolvePackage(key)
	if err != nil {
		return nil, err
	}
	return &resolved.Manifest, nil
}

// PlatformManifest is the normalized exact-platform view used by the
// registry reader; it is never decoded from YAML.
type PlatformManifest struct {
	ReleaseHistory ReleaseHistory
	Application    Application
	Desktop        Desktop
}

func (d *Document) resolvePlatform(platforms map[string]PlatformManifest, key string) (Manifest, error) {
	platform, supported := ParsePlatformKey(key)
	if !supported {
		return Manifest{}, fmt.Errorf("unsupported platform %q", key)
	}
	definition, available := platforms[key]
	if !available {
		return Manifest{}, fmt.Errorf("platform %q is unavailable", key)
	}
	result := Manifest{
		Schema: d.Schema,
		ID:     d.ID, Name: d.Name, Summary: d.Summary, Homepage: d.Homepage,
		Categories: append([]string(nil), d.Categories...), Requirements: append([]string(nil), d.Requirements...), Platform: platform,
		ReleaseHistory: copyReleaseHistory(definition.ReleaseHistory),
		Application:    copyApplication(definition.Application),
		Desktop:        copyDesktop(definition.Desktop),
	}
	for index := range result.Application.Executables {
		if result.Application.Executables[index].Name == "" {
			result.Application.Executables[index].Name = path.Base(result.Application.Executables[index].Path)
		}
	}
	var err error
	result.Release, err = result.ReleaseHistory.ResolveDefault()
	if err != nil {
		return Manifest{}, err
	}
	return result, nil
}

func (d *Document) derivedPlatforms() (map[string]PlatformManifest, error) {
	if len(d.Release.Releases) == 0 {
		return nil, errors.New("release.releases must be a non-empty sequence")
	}
	multiChannel := d.Release.DefaultChannel != "" || len(d.Release.Channels) > 0
	if multiChannel {
		if d.Release.Current != "" || d.Release.DefaultChannel == "" || !ValidChannel(d.Release.DefaultChannel) || len(d.Release.Channels) == 0 {
			return nil, errors.New("multi-channel release form requires default-channel and channels only")
		}
	} else if d.Release.Current == "" {
		return nil, errors.New("single-channel release form requires current")
	}
	result := make(map[string]PlatformManifest)
	seenReleases := make(map[string]bool, len(d.Release.Releases))
	for index, definition := range d.Release.Releases {
		if definition.Version == "" || (!multiChannel && definition.Channel != "") || (multiChannel && !ValidChannel(definition.Channel)) {
			return nil, fmt.Errorf("invalid release at index %d", index)
		}
		releaseKey := definition.Channel + "\x00" + definition.Version
		if seenReleases[releaseKey] {
			return nil, fmt.Errorf("duplicate release %q", definition.Version)
		}
		seenReleases[releaseKey] = true
		if len(definition.Artifacts) == 0 {
			return nil, fmt.Errorf("release %q artifacts must be a non-empty mapping", definition.Version)
		}
		for key, artifact := range definition.Artifacts {
			if _, ok := ParsePlatformKey(key); !ok {
				return nil, fmt.Errorf("unsupported platform %q", key)
			}
			archive, verification, err := resolveArtifactApproval(d.Release, artifact)
			if err != nil {
				return nil, fmt.Errorf("release %q artifact %s: %w", definition.Version, key, err)
			}
			release := Release{Channel: definition.Channel, Version: definition.Version, URL: artifact.URL, Verification: verification, Archive: archive}
			if definition.NestedArchive != nil {
				release.NestedArchive = *definition.NestedArchive
			}
			platform := result[key]
			platform.ReleaseHistory.Releases = append(platform.ReleaseHistory.Releases, release)
			result[key] = platform
		}
	}
	if len(result) == 0 {
		return nil, errors.New("release artifacts contain no supported platforms")
	}
	for key, platform := range result {
		application, err := d.Application.resolve(key)
		if err != nil {
			return nil, fmt.Errorf("platform %s application: %w", key, err)
		}
		platform.Application = application
		if d.Desktop != nil {
			platform.Desktop = copyDesktopDefinition(*d.Desktop)
			platform.Desktop.Enabled = true
		}
		if multiChannel {
			platform.ReleaseHistory.DefaultChannel = d.Release.DefaultChannel
			platform.ReleaseHistory.Channels = copyChannelHeads(d.Release.Channels)
		} else {
			platform.ReleaseHistory.DefaultChannel = "stable"
			platform.ReleaseHistory.Channels = map[string]ChannelHead{"stable": {Current: d.Release.Current}}
			for index := range platform.ReleaseHistory.Releases {
				platform.ReleaseHistory.Releases[index].Channel = "stable"
			}
		}
		result[key] = platform
	}
	return result, nil
}

func resolveArtifactApproval(release ReleaseDocument, artifact Artifact) (string, Verification, error) {
	if release.Verification.Algorithm == "" {
		return "", Verification{}, errors.New("release.verification.algorithm is required")
	}
	if release.Archive != "" && artifact.Archive != "" {
		return "", Verification{}, errors.New("common release archive and per-artifact archive must not be mixed")
	}
	archive := release.Archive
	if archive == "" {
		archive = artifact.Archive
	}
	if archive == "" {
		return "", Verification{}, errors.New("archive is required at release or artifact scope")
	}
	if artifact.Verification.Digest == "" || artifact.Verification.Source == "" {
		return "", Verification{}, errors.New("artifact verification requires digest and source")
	}
	return archive, Verification{Algorithm: release.Verification.Algorithm, Digest: artifact.Verification.Digest, Source: artifact.Verification.Source}, nil
}

func copyChannelHeads(values map[string]ChannelHead) map[string]ChannelHead {
	copy := make(map[string]ChannelHead, len(values))
	for channel, head := range values {
		copy[channel] = head
	}
	return copy
}

func (d ApplicationDefinition) resolve(platform string) (Application, error) {
	if (d.Executable == nil) == (len(d.Executables) == 0) {
		return Application{}, errors.New("exactly one of application.executable or application.executables is required")
	}
	definitions := d.Executables
	if d.Executable != nil {
		definitions = []ExecutableDefinition{*d.Executable}
	}
	result := Application{Executables: make([]Executable, 0, len(definitions))}
	for _, definition := range definitions {
		if (definition.Path == "") == (len(definition.Paths) == 0) {
			return Application{}, errors.New("executable requires exactly one of path or paths")
		}
		resolvedPath := definition.Path
		if resolvedPath == "" {
			var ok bool
			resolvedPath, ok = definition.Paths[platform]
			if !ok {
				return Application{}, fmt.Errorf("executable paths missing platform %q", platform)
			}
			for key := range definition.Paths {
				if _, valid := ParsePlatformKey(key); !valid {
					return Application{}, fmt.Errorf("unsupported executable path platform %q", key)
				}
			}
		}
		name := definition.Name
		if name == "" {
			name = path.Base(resolvedPath)
		}
		result.Executables = append(result.Executables, Executable{Name: name, Path: resolvedPath, CreateBinLink: definition.CreateBinLink})
	}
	return result, nil
}

func copyReleaseHistory(history ReleaseHistory) ReleaseHistory {
	copy := history
	copy.Channels = make(map[string]ChannelHead, len(history.Channels))
	for channel, head := range history.Channels {
		copy.Channels[channel] = head
	}
	copy.Releases = append([]Release(nil), history.Releases...)
	return copy
}

func copyApplication(application Application) Application {
	copy := application
	copy.Executables = append([]Executable(nil), application.Executables...)
	for index, executable := range copy.Executables {
		if executable.CreateBinLink != nil {
			value := *executable.CreateBinLink
			copy.Executables[index].CreateBinLink = &value
		}
	}
	return copy
}

func copyDesktop(desktop Desktop) Desktop {
	copy := desktop
	copy.Categories = append([]string(nil), desktop.Categories...)
	return copy
}

func copyDesktopDefinition(desktop DesktopDefinition) Desktop {
	result := Desktop{Executable: desktop.Executable, WorkingDirectory: desktop.WorkingDirectory, Categories: append([]string(nil), desktop.Categories...)}
	if desktop.Icon != nil {
		result.Icon = *desktop.Icon
	}
	return result
}

type Release struct {
	Channel       string        `yaml:"channel" json:"channel"`
	Version       string        `yaml:"version" json:"version"`
	URL           string        `yaml:"url" json:"url"`
	Verification  Verification  `yaml:"verification" json:"verification"`
	Archive       string        `yaml:"archive" json:"archive"`
	NestedArchive NestedArchive `yaml:"nested-archive,omitempty" json:"nested_archive,omitempty"`
}

type NestedArchive struct {
	Path    string `yaml:"path" json:"path"`
	Archive string `yaml:"archive" json:"archive"`
}

func (n NestedArchive) IsZero() bool { return n.Path == "" && n.Archive == "" }

// ReleaseHistory stores all releases explicitly approved for one platform.
// Channel heads are opaque version identifiers, never inferred by sorting.
type ReleaseHistory struct {
	DefaultChannel string                 `yaml:"default-channel" json:"default_channel"`
	Channels       map[string]ChannelHead `yaml:"channels" json:"channels"`
	Releases       []Release              `yaml:"releases" json:"releases"`
}

type ChannelHead struct {
	Current string `yaml:"current" json:"current"`
}

type Verification struct {
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	Digest    string `yaml:"digest" json:"digest"`
	// Source records the official upstream release or artifact origin associated
	// with the approved bytes. It is informational metadata, not proof that
	// upstream published a separate checksum file, and is never fetched by
	// TarLink at runtime.
	Source string `yaml:"source" json:"source"`
}

type Application struct {
	Executables []Executable `yaml:"executables" json:"executables"`
}

type Executable struct {
	Name          string `yaml:"name,omitempty" json:"name,omitempty"`
	Path          string `yaml:"path" json:"path"`
	CreateBinLink *bool  `yaml:"create-bin-link,omitempty" json:"create_bin_link,omitempty"`
}

// WantsBinLink preserves the schema default: an omitted create-bin-link is true.
func (e Executable) WantsBinLink() bool { return e.CreateBinLink == nil || *e.CreateBinLink }

type Desktop struct {
	Enabled          bool        `yaml:"enabled" json:"enabled"`
	Executable       string      `yaml:"executable,omitempty" json:"executable,omitempty"`
	WorkingDirectory string      `yaml:"working-directory,omitempty" json:"working_directory,omitempty"`
	Categories       []string    `yaml:"categories" json:"categories"`
	Icon             DesktopIcon `yaml:"icon" json:"icon"`
}

// DesktopIcon declares a desktop icon either as a path inside the extracted
// application tree or as a verified remote PNG. Exactly one form is allowed;
// a remote icon requires an HTTPS URL, a lowercase SHA-256 digest, and a PNG
// extension. The hicolor raster size of a remote icon is validated from the
// downloaded PNG header at install time, never from the URL path.
type DesktopIcon struct {
	Path   string `yaml:"path,omitempty" json:"path,omitempty"`
	URL    string `yaml:"url,omitempty" json:"url,omitempty"`
	SHA256 string `yaml:"sha256,omitempty" json:"sha256,omitempty"`
}

// IsZero reports whether no icon is declared.
func (i DesktopIcon) IsZero() bool { return i.Path == "" && i.URL == "" && i.SHA256 == "" }

// Remote reports whether the icon is a verified remote PNG rather than an
// archive-contained path.
func (i DesktopIcon) Remote() bool { return i.URL != "" }

func Parse(r io.Reader) (*Document, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("manifest exceeds %d bytes", MaxManifestBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("manifest is empty")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode YAML: %w", err)
	}
	if err := validateYAMLNode(&document); err != nil {
		return nil, err
	}
	if err := validateManifestShape(&document); err != nil {
		return nil, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var result Document
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("manifest must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if err := result.validateShared(); err != nil {
		return nil, err
	}
	platforms, err := result.derivedPlatforms()
	if err != nil {
		return nil, err
	}
	result.Platforms = platforms

	keys := make([]string, 0, len(platforms))
	for key := range platforms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		resolved, err := result.resolvePlatform(platforms, key)
		if err != nil {
			return nil, err
		}
		if err := resolved.Validate(); err != nil {
			return nil, fmt.Errorf("platform %s: %w", key, err)
		}
		if err := resolved.ValidateHistory(); err != nil {
			return nil, fmt.Errorf("platform %s: %w", key, err)
		}
	}
	return &result, nil
}

func (d Document) validateShared() error {
	if d.Schema != SchemaV5 {
		return fmt.Errorf("unsupported manifest schema %d", d.Schema)
	}
	if !ValidID(d.ID) {
		return fmt.Errorf("invalid application ID %q", d.ID)
	}
	if err := constrainedText("name", d.Name, 1, 120); err != nil {
		return err
	}
	if err := constrainedText("summary", d.Summary, 1, 240); err != nil {
		return err
	}
	if err := validateHTTPSURL("homepage", d.Homepage); err != nil {
		return err
	}
	if len(d.Categories) == 0 {
		return errors.New("at least one category is required")
	}
	if err := validateEnumList("category", d.Categories, map[string]bool{
		"game-development": true, "emulation": true, "graphics": true,
		"development": true, "utilities": true, "games": true, "recompilation": true,
	}); err != nil {
		return err
	}
	return validateEnumList("requirement", d.Requirements, map[string]bool{"original-game-data": true})
}

func ParseBytes(data []byte) (*Document, error) { return Parse(bytes.NewReader(data)) }

func validateYAMLNode(node *yaml.Node) error {
	if node == nil {
		return errors.New("invalid empty YAML node")
	}
	if node.Kind == yaml.AliasNode || node.Alias != nil || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not allowed")
	}
	allowedTags := map[string]bool{
		"": true, "!!map": true, "!!seq": true, "!!str": true,
		"!!int": true, "!!bool": true, "!!null": true,
	}
	if !allowedTags[node.Tag] {
		return fmt.Errorf("YAML tag %q is not allowed", node.Tag)
	}
	if node.Tag == "!!merge" || node.Value == "<<" {
		return errors.New("YAML merge keys are not allowed")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind == yaml.ScalarNode {
				if seen[key.Value] {
					return fmt.Errorf("mapping contains duplicate field %q", key.Value)
				}
				seen[key.Value] = true
			}
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func validateManifestShape(document *yaml.Node) error {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return errors.New("manifest must contain one mapping document")
	}
	root, err := requiredMapping(document.Content[0], "manifest", []string{
		"schema", "id", "name", "summary", "homepage", "categories", "release", "application",
	}, []string{"requirements", "desktop"})
	if err != nil {
		return err
	}
	release, err := requiredMapping(root["release"], "release", []string{"verification", "releases"}, []string{"current", "default-channel", "channels", "archive"})
	if err != nil {
		return err
	}
	if verification, err := requiredMapping(release["verification"], "release.verification", []string{"algorithm"}, nil); err != nil {
		return err
	} else if verification["algorithm"].Kind != yaml.ScalarNode || verification["algorithm"].Tag != "!!str" || verification["algorithm"].Value == "" {
		return errors.New("release.verification.algorithm must be a non-empty string")
	}
	if archive, ok := release["archive"]; ok && (archive.Kind != yaml.ScalarNode || archive.Tag != "!!str" || archive.Value == "") {
		return errors.New("release.archive must be a non-empty string")
	}
	if current, hasCurrent := release["current"]; hasCurrent && (current.Kind != yaml.ScalarNode || current.Tag != "!!str" || current.Value == "") {
		return errors.New("release.current must be a non-empty string")
	}
	if defaultChannel, hasDefault := release["default-channel"]; hasDefault && (defaultChannel.Kind != yaml.ScalarNode || defaultChannel.Tag != "!!str" || defaultChannel.Value == "") {
		return errors.New("release.default-channel must be a non-empty string")
	}
	if channels, hasChannels := release["channels"]; hasChannels && (channels.Kind != yaml.MappingNode || len(channels.Content) == 0) {
		return errors.New("release.channels must be a non-empty mapping")
	}
	if _, hasCurrent := release["current"]; !hasCurrent && (release["default-channel"] == nil || release["channels"] == nil) {
		return errors.New("release must declare current or the expanded channel form")
	}
	releases := release["releases"]
	if releases.Kind != yaml.SequenceNode || len(releases.Content) == 0 {
		return errors.New("releases must be a non-empty sequence")
	}
	for index, value := range releases.Content {
		entry, err := requiredMapping(value, fmt.Sprintf("release.releases[%d]", index), []string{"version", "artifacts"}, []string{"channel", "nested-archive"})
		if err != nil {
			return err
		}
		if nested, ok := entry["nested-archive"]; ok {
			if _, err := requiredMapping(nested, "release.releases.nested-archive", []string{"archive", "path"}, nil); err != nil {
				return err
			}
		}
		artifacts := entry["artifacts"]
		if artifacts.Kind != yaml.MappingNode || len(artifacts.Content) == 0 {
			return fmt.Errorf("releases[%d].artifacts must be a non-empty mapping", index)
		}
		for artifactIndex := 0; artifactIndex < len(artifacts.Content); artifactIndex += 2 {
			platform := artifacts.Content[artifactIndex]
			if platform.Kind != yaml.ScalarNode || platform.Tag != "!!str" {
				return errors.New("artifact platform keys must be strings")
			}
			if _, ok := ParsePlatformKey(platform.Value); !ok {
				return fmt.Errorf("unsupported platform %q", platform.Value)
			}
			artifact, err := requiredMapping(artifacts.Content[artifactIndex+1], "artifact."+platform.Value, []string{"url", "verification"}, []string{"archive"})
			if err != nil {
				return err
			}
			if _, err := requiredMapping(artifact["verification"], "artifact.verification", []string{"digest", "source"}, nil); err != nil {
				return err
			}
		}
	}
	app, err := requiredMapping(root["application"], "application", nil, []string{"executable", "executables"})
	if err != nil {
		return err
	}
	if (app["executable"] == nil) == (app["executables"] == nil) {
		return errors.New("application must declare exactly one of executable or executables")
	}
	validateExecutableDefinition := func(node *yaml.Node, label string) error {
		fields, err := requiredMapping(node, label, nil, []string{"name", "path", "paths", "create-bin-link"})
		if err != nil {
			return err
		}
		if (fields["path"] == nil) == (fields["paths"] == nil) {
			return fmt.Errorf("%s must declare exactly one of path or paths", label)
		}
		if paths := fields["paths"]; paths != nil {
			if paths.Kind != yaml.MappingNode || len(paths.Content) == 0 {
				return fmt.Errorf("%s.paths must be a non-empty mapping", label)
			}
			for i := 0; i < len(paths.Content); i += 2 {
				if paths.Content[i].Kind != yaml.ScalarNode || paths.Content[i].Value != PlatformLinuxAMD64 && paths.Content[i].Value != PlatformLinuxARM64 {
					return fmt.Errorf("%s.paths contains unsupported platform", label)
				}
			}
		}
		return nil
	}
	if executable := app["executable"]; executable != nil {
		if err := validateExecutableDefinition(executable, "application.executable"); err != nil {
			return err
		}
	}
	if executables := app["executables"]; executables != nil {
		if executables.Kind != yaml.SequenceNode || len(executables.Content) == 0 {
			return errors.New("application.executables must be a non-empty sequence")
		}
		for i, executable := range executables.Content {
			if err := validateExecutableDefinition(executable, fmt.Sprintf("application.executables[%d]", i)); err != nil {
				return err
			}
		}
	}
	if desktop, ok := root["desktop"]; ok {
		fields, err := requiredMapping(desktop, "desktop", nil, []string{"categories", "executable", "working-directory", "icon"})
		if err != nil {
			return err
		}
		if fields["categories"] == nil || fields["categories"].Kind != yaml.SequenceNode || len(fields["categories"].Content) == 0 {
			return errors.New("desktop.categories must be a non-empty sequence")
		}
		if icon := fields["icon"]; icon != nil {
			if icon.Tag == "!!null" {
				return errors.New("desktop.icon must be omitted when no icon is declared")
			}
			if _, err := requiredMapping(icon, "desktop.icon", nil, []string{"path", "url", "sha256"}); err != nil {
				return err
			}
		}
	}
	if requirements, ok := root["requirements"]; ok {
		if requirements.Kind != yaml.SequenceNode || len(requirements.Content) == 0 {
			return errors.New("requirements must be a non-empty sequence")
		}
		for _, requirement := range requirements.Content {
			if requirement.Kind != yaml.ScalarNode || requirement.Tag != "!!str" {
				return errors.New("requirements must contain strings")
			}
		}
	}
	return nil
}

func requiredMapping(node *yaml.Node, label string, required, optional []string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, fmt.Errorf("%s must be a mapping", label)
	}
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = true
	}
	for _, key := range optional {
		allowed[key] = true
	}
	values := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" || !allowed[key.Value] {
			return nil, fmt.Errorf("%s contains unknown field %q", label, key.Value)
		}
		if _, duplicate := values[key.Value]; duplicate {
			return nil, fmt.Errorf("%s contains duplicate field %q", label, key.Value)
		}
		values[key.Value] = node.Content[index+1]
	}
	for _, key := range required {
		if _, exists := values[key]; !exists {
			return nil, fmt.Errorf("%s is missing required field %q", label, key)
		}
	}
	return values, nil
}

func (m Manifest) Validate() error {
	if m.Schema != SchemaV5 {
		return fmt.Errorf("unsupported manifest schema %d", m.Schema)
	}
	if !ValidID(m.ID) {
		return fmt.Errorf("invalid application ID %q", m.ID)
	}
	if err := constrainedText("name", m.Name, 1, 120); err != nil {
		return err
	}
	if err := constrainedText("summary", m.Summary, 1, 240); err != nil {
		return err
	}
	if err := validateHTTPSURL("homepage", m.Homepage); err != nil {
		return err
	}
	if len(m.Categories) == 0 {
		return errors.New("at least one category is required")
	}
	if err := validateEnumList("category", m.Categories, map[string]bool{
		"game-development": true, "emulation": true, "graphics": true,
		"development": true, "utilities": true, "games": true, "recompilation": true,
	}); err != nil {
		return err
	}
	if err := validateEnumList("requirement", m.Requirements, map[string]bool{"original-game-data": true}); err != nil {
		return err
	}
	if m.Platform.OS != "linux" {
		return fmt.Errorf("unsupported operating system %q", m.Platform.OS)
	}
	if m.Platform.Arch != "amd64" && m.Platform.Arch != "arm64" {
		return fmt.Errorf("unsupported architecture %q", m.Platform.Arch)
	}
	if m.Release.Version != "" {
		if err := validateRelease(m.Release); err != nil {
			return err
		}
		if err := validateApplicationRelease(m, m.Release); err != nil {
			return err
		}
	}
	return nil
}

// ResolvedPackageFingerprint returns a stable SHA-256 identity for the
// effective package. It deliberately excludes ReleaseHistory because history
// is resolution input, not an install input; the selected Release is included
// instead. YAML formatting, map iteration order, omitted executable names,
// and omitted create-bin-link defaults therefore cannot change the result.
//
// The encoded input is a private, length-delimited binary format with a
// versioned domain separator. Every string is UTF-8 as already enforced by
// Validate, integers are big-endian, and set-like metadata lists are sorted
// before encoding. The returned form is self-describing: sha256:<lowercase
// hexadecimal digest>.
func (m Manifest) ResolvedPackageFingerprint() (string, error) {
	if err := m.Validate(); err != nil {
		return "", err
	}
	if m.Release.Version == "" {
		return "", errors.New("resolved package has no selected release")
	}
	if err := m.ValidateHistory(); err != nil {
		return "", err
	}

	var encoded bytes.Buffer
	encoded.WriteString("tarlink/resolved-package/v1\x00")
	// Schema is contract metadata, not a package input. It must not create a
	// second payload for the same resolved package.
	writeFingerprintString(&encoded, m.ID)
	if m.Desktop.Enabled {
		// The application name is materialized in the desktop entry. Without
		// desktop integration it is informational metadata and must not alter
		// the installed package identity.
		writeFingerprintString(&encoded, m.Name)
	} else {
		writeFingerprintString(&encoded, "")
	}
	writeFingerprintString(&encoded, m.Platform.OS)
	writeFingerprintString(&encoded, m.Platform.Arch)

	writeFingerprintString(&encoded, m.Release.Channel)
	writeFingerprintString(&encoded, m.Release.Version)
	writeFingerprintString(&encoded, m.Release.URL)
	writeFingerprintString(&encoded, m.Release.Verification.Algorithm)
	writeFingerprintString(&encoded, m.Release.Verification.Digest)
	writeFingerprintString(&encoded, m.Release.Archive)
	writeFingerprintString(&encoded, m.Release.NestedArchive.Path)
	writeFingerprintString(&encoded, m.Release.NestedArchive.Archive)

	encoded.WriteByte(0xA1)
	writeFingerprintInt(&encoded, uint64(len(m.Application.Executables)))
	for _, executable := range m.Application.Executables {
		writeFingerprintString(&encoded, executable.Name)
		writeFingerprintString(&encoded, executable.Path)
		if executable.WantsBinLink() {
			encoded.WriteByte(1)
		} else {
			encoded.WriteByte(0)
		}
	}

	if m.Desktop.Enabled {
		encoded.WriteByte(1)
	} else {
		encoded.WriteByte(0)
	}
	desktopExecutable := ""
	if m.Desktop.Enabled {
		desktopExecutable = m.Desktop.Executable
		if desktopExecutable == "" && len(m.Application.Executables) == 1 {
			desktopExecutable = m.Application.Executables[0].Name
		}
	}
	writeFingerprintString(&encoded, desktopExecutable)
	writeFingerprintString(&encoded, m.Desktop.WorkingDirectory)
	writeFingerprintStrings(&encoded, m.Desktop.Categories)
	writeFingerprintString(&encoded, m.Desktop.Icon.Path)
	writeFingerprintString(&encoded, m.Desktop.Icon.URL)
	writeFingerprintString(&encoded, m.Desktop.Icon.SHA256)

	digest := sha256.Sum256(encoded.Bytes())
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Fingerprint is the resolved-package spelling kept convenient for callers
// that already hold a validated ResolvedPackage.
func (p ResolvedPackage) FingerprintValue() (string, error) {
	if p.Fingerprint != "" {
		return p.Fingerprint, nil
	}
	return p.Manifest.ResolvedPackageFingerprint()
}

func writeFingerprintInt(buffer *bytes.Buffer, value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	buffer.Write(data[:])
}

func writeFingerprintString(buffer *bytes.Buffer, value string) {
	writeFingerprintInt(buffer, uint64(len(value)))
	buffer.WriteString(value)
}

func writeFingerprintStrings(buffer *bytes.Buffer, values []string) {
	writeFingerprintInt(buffer, uint64(len(values)))
	for _, value := range values {
		writeFingerprintString(buffer, value)
	}
}

func validateRelease(m Release) error {
	if err := constrainedText("release version", m.Version, 1, 128); err != nil {
		return err
	}
	if strings.ContainsAny(m.Version, `/\\`) || m.Version == "." || m.Version == ".." {
		return errors.New("release version is not filesystem-safe")
	}
	if err := validateHTTPSURL("release URL", m.URL); err != nil {
		return err
	}
	if err := validateHTTPSURL("release verification source", m.Verification.Source); err != nil {
		return err
	}
	if err := ValidateDigest(m.Verification.Algorithm, m.Verification.Digest); err != nil {
		return err
	}
	switch m.Archive {
	case "tar.gz", "tar.xz", "zip", "appimage":
	default:
		return fmt.Errorf("unsupported archive format %q", m.Archive)
	}
	if !m.NestedArchive.IsZero() {
		if m.Archive == "appimage" {
			return errors.New("AppImage releases cannot contain nested archives")
		}
		if err := ValidateRelativePath(m.NestedArchive.Path); err != nil {
			return fmt.Errorf("invalid nested archive path: %w", err)
		}
		switch m.NestedArchive.Archive {
		case "tar.gz", "tar.xz", "zip":
		default:
			return fmt.Errorf("unsupported nested archive format %q", m.NestedArchive.Archive)
		}
	}
	return nil
}

func validateApplicationRelease(m Manifest, release Release) error {
	if len(m.Application.Executables) == 0 {
		return errors.New("application.executables must not be empty")
	}
	seenNames := make(map[string]bool, len(m.Application.Executables))
	for _, executable := range m.Application.Executables {
		if executable.Name == "" || strings.ContainsAny(executable.Name, `/\\`) || executable.Name == "." || executable.Name == ".." || strings.TrimSpace(executable.Name) != executable.Name {
			return fmt.Errorf("invalid executable name %q", executable.Name)
		}
		if seenNames[executable.Name] {
			return fmt.Errorf("duplicate executable name %q", executable.Name)
		}
		seenNames[executable.Name] = true
		if err := ValidateRelativePath(executable.Path); err != nil {
			return fmt.Errorf("invalid executable path for %q: %w", executable.Name, err)
		}
		if release.Archive == "appimage" && executable.Path != "appimage" {
			return fmt.Errorf("AppImage executable %q must target appimage", executable.Name)
		}
	}
	if hasCategory(m.Categories, "games") || hasCategory(m.Categories, "recompilation") {
		for index, executable := range m.Application.Executables {
			if executable.CreateBinLink == nil {
				return fmt.Errorf("application.executables[%d].create-bin-link must be explicit for games and recompilations", index)
			}
		}
	}
	if m.Desktop.Executable != "" {
		matched := 0
		for _, executable := range m.Application.Executables {
			if executable.Name == m.Desktop.Executable {
				matched++
			}
		}
		if matched != 1 {
			return fmt.Errorf("desktop executable %q does not resolve uniquely", m.Desktop.Executable)
		}
	} else if m.Desktop.Enabled && len(m.Application.Executables) != 1 {
		return errors.New("desktop executable is required when multiple executables are declared")
	}
	if m.Desktop.WorkingDirectory != "" && m.Desktop.WorkingDirectory != "application-root" {
		return fmt.Errorf("unsupported desktop working-directory %q", m.Desktop.WorkingDirectory)
	}
	if release.Archive == "appimage" && m.Desktop.Icon.Path != "" {
		return errors.New("AppImage releases cannot declare archive-contained desktop icons")
	}
	if m.Desktop.Enabled && len(m.Desktop.Categories) == 0 {
		return errors.New("desktop categories are required when desktop integration is enabled")
	}
	if !m.Desktop.Icon.IsZero() {
		if !m.Desktop.Enabled {
			return errors.New("desktop icon requires desktop integration")
		}
		if err := m.Desktop.Icon.validate(); err != nil {
			return err
		}
	}
	if err := validateEnumList("desktop category", m.Desktop.Categories, map[string]bool{
		"Development": true, "Emulator": true, "Game": true,
		"Graphics": true, "Utility": true,
	}); err != nil {
		return err
	}
	return nil
}

func hasCategory(categories []string, wanted string) bool {
	for _, category := range categories {
		if category == wanted {
			return true
		}
	}
	return false
}

func (i DesktopIcon) validate() error {
	if i.Path == "" && i.URL == "" {
		return errors.New("desktop icon must declare a path or a verified remote URL")
	}
	if i.Path != "" {
		if i.URL != "" || i.SHA256 != "" {
			return errors.New("desktop icon cannot mix a path with remote icon fields")
		}
		return ValidateRelativePath(i.Path)
	}
	if err := validateHTTPSURL("desktop icon URL", i.URL); err != nil {
		return err
	}
	if err := ValidateDigest("sha256", i.SHA256); err != nil {
		return fmt.Errorf("desktop icon sha256: %w", err)
	}
	parsed, err := url.Parse(i.URL)
	if err != nil {
		return fmt.Errorf("invalid desktop icon URL: %w", err)
	}
	if !strings.EqualFold(path.Ext(parsed.Path), ".png") {
		return errors.New("desktop icon URL must reference a PNG file")
	}
	return nil
}

// supportedHicolorSizes are the raster sizes TarLink places into the XDG
// hicolor hierarchy. Remote PNG icons must be exactly one of these sizes;
// the size is validated from the PNG header, never decoded from image pixels.
var supportedHicolorSizes = map[int]bool{
	16: true, 22: true, 24: true, 32: true, 48: true,
	64: true, 96: true, 128: true, 256: true, 512: true,
}

// ValidHicolorSize reports whether size is a supported hicolor raster size.
func ValidHicolorSize(size int) bool { return size > 0 && supportedHicolorSizes[size] }

// pngSignature is the fixed 8-byte PNG file signature.
var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// IconSizeFromPNG validates the PNG signature and IHDR header of downloaded
// icon bytes without decoding image pixels and returns the square supported
// hicolor raster size. Non-PNG bytes, malformed headers, and unsupported or
// non-square dimensions are rejected.
func IconSizeFromPNG(data []byte) (int, error) {
	if len(data) < 24 {
		return 0, errors.New("desktop icon is not a PNG")
	}
	if !bytes.Equal(data[:8], pngSignature) {
		return 0, errors.New("desktop icon is not a PNG")
	}
	if binary.BigEndian.Uint32(data[8:12]) != 13 || string(data[12:16]) != "IHDR" {
		return 0, errors.New("desktop icon is not a PNG")
	}
	width := int(binary.BigEndian.Uint32(data[16:20]))
	height := int(binary.BigEndian.Uint32(data[20:24]))
	if width <= 0 || height <= 0 {
		return 0, errors.New("desktop icon has invalid PNG dimensions")
	}
	if width != height {
		return 0, fmt.Errorf("desktop icon is not square (%dx%d)", width, height)
	}
	if !ValidHicolorSize(width) {
		return 0, fmt.Errorf("desktop icon has unsupported raster size %dx%d", width, height)
	}
	return width, nil
}

func (m Manifest) ValidateHistory() error {
	if m.ReleaseHistory.DefaultChannel == "" || !ValidChannel(m.ReleaseHistory.DefaultChannel) {
		return fmt.Errorf("invalid default channel %q", m.ReleaseHistory.DefaultChannel)
	}
	if len(m.ReleaseHistory.Channels) == 0 {
		return errors.New("at least one release channel is required")
	}
	// Selectors use the same app@value syntax for channels and exact
	// versions.  A version that is also a channel name would therefore be
	// impossible to address unambiguously (the registry resolver must prefer
	// the channel interpretation). Reject that ambiguity at the manifest
	// boundary rather than relying on resolver ordering.
	for _, release := range m.ReleaseHistory.Releases {
		if _, ok := m.ReleaseHistory.Channels[release.Version]; ok {
			return fmt.Errorf("release version %q conflicts with channel name", release.Version)
		}
	}
	seen := make(map[string]bool, len(m.ReleaseHistory.Releases))
	versions := make(map[string]string, len(m.ReleaseHistory.Releases))
	for _, release := range m.ReleaseHistory.Releases {
		if !ValidChannel(release.Channel) {
			return fmt.Errorf("invalid release channel %q", release.Channel)
		}
		if seen[release.Channel+"\x00"+release.Version] {
			return fmt.Errorf("duplicate release %q in channel %q", release.Version, release.Channel)
		}
		seen[release.Channel+"\x00"+release.Version] = true
		if previous, ok := versions[release.Version]; ok && previous != release.Channel {
			return fmt.Errorf("version %q is ambiguous across channels %q and %q", release.Version, previous, release.Channel)
		}
		versions[release.Version] = release.Channel
		if err := validateRelease(release); err != nil {
			return err
		}
		if err := validateApplicationRelease(m, release); err != nil {
			return err
		}
	}
	if _, ok := m.ReleaseHistory.Channels[m.ReleaseHistory.DefaultChannel]; !ok {
		return fmt.Errorf("default channel %q is not defined", m.ReleaseHistory.DefaultChannel)
	}
	for channel, head := range m.ReleaseHistory.Channels {
		if !ValidChannel(channel) || head.Current == "" {
			return fmt.Errorf("invalid channel head %q", channel)
		}
		count := 0
		for _, release := range m.ReleaseHistory.Releases {
			if release.Channel == channel && release.Version == head.Current {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("channel %q current release %q is not unique", channel, head.Current)
		}
	}
	return nil
}

func (h ReleaseHistory) ResolveDefault() (Release, error) {
	head, ok := h.Channels[h.DefaultChannel]
	if !ok {
		return Release{}, fmt.Errorf("default channel %q is not defined", h.DefaultChannel)
	}
	for _, release := range h.Releases {
		if release.Channel == h.DefaultChannel && release.Version == head.Current {
			return release, nil
		}
	}
	return Release{}, fmt.Errorf("default channel %q current release %q is unavailable", h.DefaultChannel, head.Current)
}

var channelPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func ValidChannel(value string) bool { return channelPattern.MatchString(value) }

func ValidID(id string) bool {
	return len(id) <= 80 && idPattern.MatchString(id)
}

func ValidateDigest(algorithm, value string) error {
	return checksum.Validate(algorithm, value)
}

// ValidateRelativePath accepts only canonical slash-separated paths below an
// extracted application root. Backslashes are rejected rather than treated as
// separators so Windows absolute paths cannot be smuggled onto Linux.
func ValidateRelativePath(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return errors.New("path is empty or invalid UTF-8")
	}
	if len(value) > 4096 {
		return errors.New("path is too long")
	}
	if strings.Contains(value, `\`) {
		return errors.New("backslashes are not allowed")
	}
	if strings.ContainsAny(value, "$%") {
		return errors.New("path interpolation syntax is not allowed")
	}
	if strings.HasPrefix(value, "/") || path.IsAbs(value) {
		return errors.New("absolute paths are not allowed")
	}
	if len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' {
		return errors.New("Windows drive paths are not allowed")
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return errors.New("path must be canonical and remain beneath the application root")
	}
	if strings.Count(cleaned, "/")+1 > 64 {
		return errors.New("path is too deep")
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("path contains an invalid component")
		}
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("path contains a control character")
		}
	}
	return nil
}

func validateHTTPSURL(field, value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s must be an HTTPS URL without user information", field)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("%s must not contain a fragment", field)
	}
	if parsed.Path != "" && path.Clean(parsed.Path) != parsed.Path {
		return fmt.Errorf("%s path must be canonical", field)
	}
	return nil
}

func constrainedText(field, value string, minRunes, maxRunes int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be valid, trimmed UTF-8", field)
	}
	length := utf8.RuneCountInString(value)
	if length < minRunes || length > maxRunes {
		return fmt.Errorf("%s has an invalid length or contains control characters", field)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s has an invalid length or contains control characters", field)
		}
	}
	return nil
}

func validateEnumList(field string, values []string, allowed map[string]bool) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !allowed[value] {
			return fmt.Errorf("unsupported %s %q", field, value)
		}
		if seen[value] {
			return fmt.Errorf("duplicate %s %q", field, value)
		}
		seen[value] = true
	}
	return nil
}
