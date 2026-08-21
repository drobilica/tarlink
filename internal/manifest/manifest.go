// Package manifest implements TarLink's deliberately small, declarative v3
// application manifest. A manifest can describe data only; it cannot request
// process execution, hooks, arbitrary destinations, or command arguments.
package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/drobilica/tarlink/internal/checksum"
	"go.yaml.in/yaml/v3"
)

const (
	SchemaV1         = 1 // retired; retained as a name for callers describing old data.
	SchemaV2         = 2
	SchemaV3         = 3
	MaxManifestBytes = 1 << 20
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Manifest struct {
	Schema       int      `yaml:"schema" json:"schema"`
	ID           string   `yaml:"id" json:"id"`
	Name         string   `yaml:"name" json:"name"`
	Summary      string   `yaml:"summary" json:"summary"`
	Homepage     string   `yaml:"homepage" json:"homepage"`
	Categories   []string `yaml:"categories" json:"categories"`
	Requirements []string `yaml:"requirements,omitempty" json:"requirements,omitempty"`
	Platform     Platform `yaml:"platform" json:"platform"`
	// Release is the selected default-channel head. It is populated from
	// ReleaseHistory by Parse and by registry resolution; it is not itself a
	// YAML field. Keeping this convenience view avoids making consumers parse
	// registry history when they only need the artifact to install.
	Release        Release        `yaml:"-" json:"release"`
	ReleaseHistory ReleaseHistory `yaml:"release" json:"release_history"`
	Application    Application    `yaml:"application" json:"application"`
	Desktop        Desktop        `yaml:"desktop" json:"desktop"`
}

type Platform struct {
	OS   string `yaml:"os" json:"os"`
	Arch string `yaml:"arch" json:"arch"`
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
	Source    string `yaml:"source" json:"source"`
}

type Application struct {
	Executables []Executable `yaml:"executables" json:"executables"`
}

type Executable struct {
	Name string `yaml:"name" json:"name"`
	Path string `yaml:"path" json:"path"`
}

type Desktop struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Categories []string `yaml:"categories" json:"categories"`
	Icon       string   `yaml:"icon" json:"icon"`
}

func Parse(r io.Reader) (*Manifest, error) {
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

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var result Manifest
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode manifest: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("manifest must contain exactly one YAML document")
		}
		return nil, fmt.Errorf("decode trailing YAML: %w", err)
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	if err := result.ValidateHistory(); err != nil {
		return nil, err
	}
	result.Release, err = result.ReleaseHistory.ResolveDefault()
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func ParseBytes(data []byte) (*Manifest, error) { return Parse(bytes.NewReader(data)) }

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
		"schema", "id", "name", "summary", "homepage", "categories", "platform", "release", "application", "desktop",
	}, []string{"requirements"})
	if err != nil {
		return err
	}
	if _, err := requiredMapping(root["platform"], "platform", []string{"os", "arch"}, nil); err != nil {
		return err
	}
	release, err := requiredMapping(root["release"], "release", []string{"default-channel", "channels", "releases"}, nil)
	if err != nil {
		return err
	}
	if release["channels"].Kind != yaml.MappingNode || len(release["channels"].Content) == 0 {
		return errors.New("release.channels must be a non-empty mapping")
	}
	for i := 0; i < len(release["channels"].Content); i += 2 {
		name := release["channels"].Content[i]
		if name.Kind != yaml.ScalarNode || name.Tag != "!!str" {
			return errors.New("release channel names must be strings")
		}
		if _, err := requiredMapping(release["channels"].Content[i+1], "release.channels."+name.Value, []string{"current"}, nil); err != nil {
			return err
		}
	}
	if release["releases"].Kind != yaml.SequenceNode || len(release["releases"].Content) == 0 {
		return errors.New("release.releases must be a non-empty sequence")
	}
	for index, value := range release["releases"].Content {
		entry, err := requiredMapping(value, fmt.Sprintf("release.releases[%d]", index), []string{"channel", "version", "url", "verification", "archive"}, []string{"nested-archive"})
		if err != nil {
			return err
		}
		if _, err := requiredMapping(entry["verification"], "release.releases.verification", []string{"algorithm", "digest", "source"}, nil); err != nil {
			return err
		}
		if nested, ok := entry["nested-archive"]; ok {
			nm, err := requiredMapping(nested, "release.releases.nested", []string{"path", "archive"}, nil)
			if err != nil {
				return err
			}
			if nm["path"].Value == "" || nm["archive"].Value == "" {
				return errors.New("release.releases.nested-archive fields must not be empty")
			}
		}
	}
	application, err := requiredMapping(root["application"], "application", []string{"executables"}, nil)
	if err != nil {
		return err
	}
	if application["executables"].Kind != yaml.SequenceNode || len(application["executables"].Content) == 0 {
		return errors.New("application.executables must be a non-empty sequence")
	}
	for index, value := range application["executables"].Content {
		if _, err := requiredMapping(value, fmt.Sprintf("application.executables[%d]", index), []string{"name", "path"}, nil); err != nil {
			return err
		}
	}
	_, err = requiredMapping(root["desktop"], "desktop", []string{"enabled"}, []string{"categories", "icon"})
	if err != nil {
		return err
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
	if m.Schema != SchemaV3 {
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
	}
	return nil
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
	if m.Verification.Source == m.URL {
		return errors.New("release verification source must be a separate checksum metadata URL")
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
	if release.Archive == "appimage" && m.Desktop.Icon != "" {
		return errors.New("AppImage releases cannot declare desktop icons")
	}
	if m.Desktop.Enabled && len(m.Desktop.Categories) == 0 {
		return errors.New("desktop categories are required when desktop integration is enabled")
	}
	if m.Desktop.Icon != "" {
		if !m.Desktop.Enabled {
			return errors.New("desktop icon requires desktop integration")
		}
		if err := ValidateRelativePath(m.Desktop.Icon); err != nil {
			return fmt.Errorf("invalid desktop icon: %w", err)
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
