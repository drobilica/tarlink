// Package manifest implements TarLink's deliberately small, declarative v1
// application manifest. A manifest can describe data only; it cannot request
// process execution, hooks, arbitrary destinations, or command arguments.
package manifest

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	SchemaV1         = 1
	MaxManifestBytes = 1 << 20
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Manifest struct {
	Schema      int         `yaml:"schema" json:"schema"`
	ID          string      `yaml:"id" json:"id"`
	Name        string      `yaml:"name" json:"name"`
	Summary     string      `yaml:"summary" json:"summary"`
	Homepage    string      `yaml:"homepage" json:"homepage"`
	Categories  []string    `yaml:"categories" json:"categories"`
	Platform    Platform    `yaml:"platform" json:"platform"`
	Release     Release     `yaml:"release" json:"release"`
	Application Application `yaml:"application" json:"application"`
	Desktop     Desktop     `yaml:"desktop" json:"desktop"`
}

type Platform struct {
	OS   string `yaml:"os" json:"os"`
	Arch string `yaml:"arch" json:"arch"`
}

type Release struct {
	Version      string       `yaml:"version" json:"version"`
	URL          string       `yaml:"url" json:"url"`
	Verification Verification `yaml:"verification" json:"verification"`
	Archive      string       `yaml:"archive" json:"archive"`
}

type Verification struct {
	Algorithm string `yaml:"algorithm" json:"algorithm"`
	Digest    string `yaml:"digest" json:"digest"`
	Source    string `yaml:"source" json:"source"`
}

type Application struct {
	Executable string `yaml:"executable" json:"executable"`
}

type Desktop struct {
	Enabled    bool     `yaml:"enabled" json:"enabled"`
	Categories []string `yaml:"categories" json:"categories"`
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
	}, nil)
	if err != nil {
		return err
	}
	if _, err := requiredMapping(root["platform"], "platform", []string{"os", "arch"}, nil); err != nil {
		return err
	}
	release, err := requiredMapping(root["release"], "release", []string{"version", "url", "verification", "archive"}, nil)
	if err != nil {
		return err
	}
	if _, err := requiredMapping(release["verification"], "release.verification", []string{"algorithm", "digest", "source"}, nil); err != nil {
		return err
	}
	if _, err := requiredMapping(root["application"], "application", []string{"executable"}, nil); err != nil {
		return err
	}
	_, err = requiredMapping(root["desktop"], "desktop", []string{"enabled"}, []string{"categories"})
	return err
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
	if m.Schema != SchemaV1 {
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
		"development": true, "utilities": true,
	}); err != nil {
		return err
	}
	if m.Platform.OS != "linux" {
		return fmt.Errorf("unsupported operating system %q", m.Platform.OS)
	}
	if m.Platform.Arch != "amd64" && m.Platform.Arch != "arm64" {
		return fmt.Errorf("unsupported architecture %q", m.Platform.Arch)
	}
	if err := constrainedText("release version", m.Release.Version, 1, 128); err != nil {
		return err
	}
	if strings.ContainsAny(m.Release.Version, `/\\`) || m.Release.Version == "." || m.Release.Version == ".." {
		return errors.New("release version is not filesystem-safe")
	}
	if err := validateHTTPSURL("release URL", m.Release.URL); err != nil {
		return err
	}
	if err := validateHTTPSURL("release verification source", m.Release.Verification.Source); err != nil {
		return err
	}
	if m.Release.Verification.Source == m.Release.URL {
		return errors.New("release verification source must be a separate checksum metadata URL")
	}
	if err := ValidateDigest(m.Release.Verification.Algorithm, m.Release.Verification.Digest); err != nil {
		return err
	}
	switch m.Release.Archive {
	case "tar.gz", "tar.xz", "zip":
	default:
		return fmt.Errorf("unsupported archive format %q", m.Release.Archive)
	}
	if err := ValidateRelativePath(m.Application.Executable); err != nil {
		return fmt.Errorf("invalid application executable: %w", err)
	}
	if m.Desktop.Enabled && len(m.Desktop.Categories) == 0 {
		return errors.New("desktop categories are required when desktop integration is enabled")
	}
	if err := validateEnumList("desktop category", m.Desktop.Categories, map[string]bool{
		"Development": true, "Emulator": true, "Game": true,
		"Graphics": true, "Utility": true,
	}); err != nil {
		return err
	}
	return nil
}

func ValidID(id string) bool {
	return len(id) <= 80 && idPattern.MatchString(id)
}

func ValidateDigest(algorithm, value string) error {
	var size int
	switch algorithm {
	case "sha256":
		size = 32
	case "sha512":
		size = 64
	default:
		return fmt.Errorf("unsupported release verification algorithm %q", algorithm)
	}
	if len(value) != size*2 || strings.ToLower(value) != value {
		return fmt.Errorf("release verification digest must be exactly %d lowercase hexadecimal characters", size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return fmt.Errorf("release verification digest must be exactly %d lowercase hexadecimal characters", size*2)
	}
	return nil
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
