// Package state persists TarLink's schema-2 per-application state record.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/manifest"
)

const Schema = 2

type Integration struct {
	Executables        []ExecutableIntegration `json:"executables,omitempty"`
	DesktopEntry       string                  `json:"desktop_entry"`
	DesktopSHA256      string                  `json:"desktop_sha256"`
	DesktopExecutable  string                  `json:"desktop_executable,omitempty"`
	WorkingDirectory   bool                    `json:"working_directory,omitempty"`
	IconFile           string                  `json:"icon_file,omitempty"`
	IconSHA256         string                  `json:"icon_sha256,omitempty"`
	IconSize           int                     `json:"icon_size,omitempty"`
	IconSource         string                  `json:"icon_source,omitempty"`
	PreviousIconFile   string                  `json:"previous_icon_file,omitempty"`
	PreviousIconSHA256 string                  `json:"previous_icon_sha256,omitempty"`
	PreviousIconSize   int                     `json:"previous_icon_size,omitempty"`
	PreviousIconSource string                  `json:"previous_icon_source,omitempty"`
}

type ExecutableIntegration struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Link          string `json:"link,omitempty"`
	Target        string `json:"target"`
	CreateBinLink *bool  `json:"create_bin_link,omitempty"`
}

type State struct {
	Schema           int    `json:"schema"`
	App              string `json:"app"`
	Current          string `json:"current"`
	CurrentRevision  int    `json:"current_revision,omitempty"`
	Previous         string `json:"previous,omitempty"`
	PreviousRevision int    `json:"previous_revision,omitempty"`
	PreviousArtifact string `json:"previous_artifact,omitempty"`
	// Channel is the channel used to resolve the current installation. It is
	// deliberately persisted as an opaque registry identifier; channels are
	// not a global enum and must not be inferred from version syntax.
	Channel         string       `json:"channel,omitempty"`
	PreviousChannel string       `json:"previous_channel,omitempty"`
	Pinned          bool         `json:"pinned"`
	Artifact        string       `json:"artifact"`
	Executables     []Executable `json:"executables"`
	DesktopEnabled  bool         `json:"desktop_enabled"`
	Integration     Integration  `json:"integration"`
}

type Executable struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	CreateBinLink *bool  `json:"create_bin_link,omitempty"`
}

func (e Executable) WantsBinLink() bool { return e.CreateBinLink == nil || *e.CreateBinLink }

var ErrCorrupt = errors.New("corrupt state")

func (s State) Validate() error {
	if s.Schema != Schema {
		return fmt.Errorf("%w: unsupported schema %d", ErrCorrupt, s.Schema)
	}
	if err := filesystem.ValidateID(s.App); err != nil {
		return fmt.Errorf("%w: app: %v", ErrCorrupt, err)
	}
	if err := filesystem.ValidateVersion(s.Current); err != nil {
		return fmt.Errorf("%w: current version: %v", ErrCorrupt, err)
	}
	currentRevision := s.CurrentRevision
	if currentRevision == 0 {
		currentRevision = 1
	}
	if currentRevision < 1 {
		return fmt.Errorf("%w: current revision must be positive", ErrCorrupt)
	}
	if s.Previous != "" {
		if err := filesystem.ValidateVersion(s.Previous); err != nil {
			return fmt.Errorf("%w: previous version: %v", ErrCorrupt, err)
		}
		previousRevision := s.PreviousRevision
		if previousRevision == 0 {
			previousRevision = 1
		}
		if previousRevision < 1 {
			return fmt.Errorf("%w: previous revision must be positive", ErrCorrupt)
		}
		if s.Previous == s.Current && previousRevision == currentRevision {
			return fmt.Errorf("%w: current and previous versions must differ", ErrCorrupt)
		}
	}
	if err := ValidateChannel(s.Channel); err != nil {
		return fmt.Errorf("%w: channel: %v", ErrCorrupt, err)
	}
	for name, channel := range map[string]string{"previous channel": s.PreviousChannel} {
		if channel == "" {
			continue
		}
		if err := ValidateChannel(channel); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrCorrupt, name, err)
		}
	}
	if s.Previous == "" && s.PreviousChannel != "" {
		return fmt.Errorf("%w: previous channel requires previous version", ErrCorrupt)
	}
	if s.Previous == "" && s.PreviousArtifact != "" {
		return fmt.Errorf("%w: previous artifact requires previous version", ErrCorrupt)
	}
	if s.Previous != "" && !validArtifact(s.PreviousArtifact) {
		return fmt.Errorf("%w: previous artifact is invalid", ErrCorrupt)
	}
	if s.Artifact != "tar.gz" && s.Artifact != "tar.xz" && s.Artifact != "zip" && s.Artifact != "appimage" {
		return fmt.Errorf("%w: unsupported artifact kind", ErrCorrupt)
	}
	if len(s.Executables) == 0 {
		return fmt.Errorf("%w: executable list is empty", ErrCorrupt)
	}
	seen := map[string]bool{}
	for _, executable := range s.Executables {
		if executable.Name == "" || strings.ContainsAny(executable.Name, `/\\`) || seen[executable.Name] {
			return fmt.Errorf("%w: invalid or duplicate executable name", ErrCorrupt)
		}
		seen[executable.Name] = true
		if err := validateExecutable(executable.Path); err != nil {
			return fmt.Errorf("%w: executable %s: %v", ErrCorrupt, executable.Name, err)
		}
	}
	if len(s.Integration.Executables) != len(s.Executables) {
		return fmt.Errorf("%w: executable integration count mismatch", ErrCorrupt)
	}
	for index, integration := range s.Integration.Executables {
		if integration.Name == "" || integration.Target == "" || (integration.WantsBinLink() && integration.Link == "") {
			return fmt.Errorf("%w: executable integration is incomplete", ErrCorrupt)
		}
		if index >= len(s.Executables) || integration.Name != s.Executables[index].Name || integration.WantsBinLink() != s.Executables[index].WantsBinLink() {
			return fmt.Errorf("%w: executable integration does not match executable state", ErrCorrupt)
		}
	}
	for name, path := range map[string]string{
		"desktop entry":      s.Integration.DesktopEntry,
		"icon file":          s.Integration.IconFile,
		"previous icon file": s.Integration.PreviousIconFile,
	} {
		if path == "" {
			continue
		}
		if !utf8.ValidString(path) || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%w: %s must be absolute and clean", ErrCorrupt, name)
		}
	}
	if s.DesktopEnabled && s.Integration.DesktopEntry == "" {
		return fmt.Errorf("%w: desktop entry required when desktop integration is enabled", ErrCorrupt)
	}
	if s.DesktopEnabled {
		if len(s.Integration.DesktopSHA256) != 64 || strings.ToLower(s.Integration.DesktopSHA256) != s.Integration.DesktopSHA256 {
			return fmt.Errorf("%w: desktop ownership digest is invalid", ErrCorrupt)
		}
		for _, value := range s.Integration.DesktopSHA256 {
			if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
				return fmt.Errorf("%w: desktop ownership digest is invalid", ErrCorrupt)
			}
		}
	}
	if !s.DesktopEnabled && (s.Integration.DesktopEntry != "" || s.Integration.DesktopSHA256 != "") {
		return fmt.Errorf("%w: desktop ownership fields must be empty when desktop integration is disabled", ErrCorrupt)
	}
	if s.Integration.IconFile != "" && (len(s.Integration.IconSHA256) != 64 || strings.ToLower(s.Integration.IconSHA256) != s.Integration.IconSHA256) {
		return fmt.Errorf("%w: icon ownership digest is invalid", ErrCorrupt)
	}
	if s.Integration.IconFile != "" {
		for _, value := range s.Integration.IconSHA256 {
			if !(value >= '0' && value <= '9' || value >= 'a' && value <= 'f') {
				return fmt.Errorf("%w: icon ownership digest is invalid", ErrCorrupt)
			}
		}
	}
	if s.Integration.IconFile == "" && s.Integration.IconSHA256 != "" {
		return fmt.Errorf("%w: icon ownership fields are inconsistent", ErrCorrupt)
	}
	if s.Integration.IconFile == "" && s.Integration.IconSource != "" {
		return fmt.Errorf("%w: icon source is inconsistent", ErrCorrupt)
	}
	if s.Integration.IconSize < 0 {
		return fmt.Errorf("%w: icon ownership size is invalid", ErrCorrupt)
	}
	if s.Integration.IconFile == "" && s.Integration.IconSize > 0 {
		return fmt.Errorf("%w: icon ownership fields are inconsistent", ErrCorrupt)
	}
	if s.Integration.IconSize > 0 && !manifest.ValidHicolorSize(s.Integration.IconSize) {
		return fmt.Errorf("%w: icon ownership size is unsupported", ErrCorrupt)
	}
	if s.Integration.IconSource != "" {
		if err := validateExecutable(s.Integration.IconSource); err != nil {
			return fmt.Errorf("%w: icon source: %v", ErrCorrupt, err)
		}
	}
	if !s.DesktopEnabled && s.Integration.IconFile != "" {
		return fmt.Errorf("%w: icon ownership fields require desktop integration", ErrCorrupt)
	}
	if s.Integration.PreviousIconFile == "" && (s.Integration.PreviousIconSHA256 != "" || s.Integration.PreviousIconSource != "") {
		return fmt.Errorf("%w: previous icon ownership fields are inconsistent", ErrCorrupt)
	}
	if s.Integration.PreviousIconSize < 0 {
		return fmt.Errorf("%w: previous icon ownership size is invalid", ErrCorrupt)
	}
	if s.Integration.PreviousIconFile == "" && s.Integration.PreviousIconSize > 0 {
		return fmt.Errorf("%w: previous icon ownership fields are inconsistent", ErrCorrupt)
	}
	if s.Integration.PreviousIconSize > 0 && !manifest.ValidHicolorSize(s.Integration.PreviousIconSize) {
		return fmt.Errorf("%w: previous icon ownership size is unsupported", ErrCorrupt)
	}
	if s.Integration.PreviousIconFile != "" {
		if !s.DesktopEnabled || len(s.Integration.PreviousIconSHA256) != 64 || strings.ToLower(s.Integration.PreviousIconSHA256) != s.Integration.PreviousIconSHA256 {
			return fmt.Errorf("%w: previous icon ownership is invalid", ErrCorrupt)
		}
		if err := validateExecutable(s.Integration.PreviousIconSource); err != nil {
			return fmt.Errorf("%w: previous icon source: %v", ErrCorrupt, err)
		}
	}
	return nil
}

func (e ExecutableIntegration) WantsBinLink() bool { return e.CreateBinLink == nil || *e.CreateBinLink }

func validArtifact(value string) bool {
	return value == "tar.gz" || value == "tar.xz" || value == "zip" || value == "appimage"
}

// ValidateChannel validates a registry-provided channel identifier. Channel
// names are intentionally opaque, but constrained to a portable, deterministic
// identifier so they are safe in selectors and state files.
func ValidateChannel(value string) error {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return errors.New("must be a non-empty trimmed identifier")
	}
	for index, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') || index == 0 && r == '-' {
			return errors.New("must contain only lowercase letters, digits, '.', '_' or '-'")
		}
	}
	return nil
}

func (s State) ValidateForLayout(l filesystem.Layout) error {
	if err := s.Validate(); err != nil {
		return err
	}
	appRoot := filepath.Join(l.Apps, s.App)
	currentRevision, previousRevision := s.CurrentRevision, s.PreviousRevision
	if currentRevision == 0 {
		currentRevision = 1
	}
	if previousRevision == 0 {
		previousRevision = 1
	}
	for _, executable := range s.Integration.Executables {
		expectedLink := filepath.Join(l.Bin, executable.Name)
		if !executable.WantsBinLink() {
			expectedLink = ""
		}
		expectedTarget := filepath.Join(appRoot, "current", filepath.FromSlash(executable.Path))
		if executable.Link != expectedLink || executable.Target != expectedTarget {
			return fmt.Errorf("%w: executable integration paths do not match canonical layout", ErrCorrupt)
		}
	}
	expectedDesktop := ""
	if s.DesktopEnabled {
		expectedDesktop = filepath.Join(l.Desktop, "tarlink-"+s.App+".desktop")
	}
	expectedIcon := ""
	if s.Integration.IconFile != "" {
		expectedIcon = filepath.Join(l.Icons, iconSizeDirectory(s.Integration.IconSize, s.Integration.IconFile), "apps", "tarlink-"+s.App+filepath.Ext(s.Integration.IconFile))
	}
	if s.Integration.IconFile != expectedIcon {
		return fmt.Errorf("%w: icon path does not match the canonical layout", ErrCorrupt)
	}
	expectedPreviousIcon := ""
	if s.Integration.PreviousIconFile != "" {
		expectedPreviousIcon = filepath.Join(l.Icons, iconSizeDirectory(s.Integration.PreviousIconSize, s.Integration.PreviousIconFile), "apps", "tarlink-"+s.App+filepath.Ext(s.Integration.PreviousIconFile))
	}
	if s.Integration.PreviousIconFile != expectedPreviousIcon {
		return fmt.Errorf("%w: previous icon path does not match the canonical layout", ErrCorrupt)
	}
	if s.Integration.DesktopEntry != expectedDesktop {
		return fmt.Errorf("%w: desktop path does not match the canonical layout", ErrCorrupt)
	}
	if _, err := l.PackagePath(s.App, s.Current, currentRevision); err != nil {
		return err
	}
	if s.Previous != "" {
		if _, err := l.PackagePath(s.App, s.Previous, previousRevision); err != nil {
			return err
		}
	}
	return nil
}

func iconSizeDirectory(size int, value string) string {
	if size > 0 {
		return fmt.Sprintf("%dx%d", size, size)
	}
	if strings.EqualFold(filepath.Ext(value), ".svg") {
		return "scalable"
	}
	return "48x48"
}

func validateExecutable(value string) error {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.ContainsAny(value, `\\$%`) || strings.HasPrefix(value, "/") || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") || strings.Count(value, "/")+1 > 64 {
		return errors.New("must be a canonical relative path")
	}
	if len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' {
		return errors.New("must not be a Windows drive path")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return errors.New("contains control character")
		}
	}
	return nil
}

// Decode strictly decodes one JSON document, rejecting unknown fields,
// duplicate object keys, trailing data, and missing required top-level fields.
func Decode(data []byte) (State, error) {
	if err := rejectDuplicateJSON(data); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	for _, required := range []string{"schema", "app", "current", "channel", "pinned", "artifact", "desktop_enabled", "integration"} {
		if _, ok := fields[required]; !ok {
			return State{}, fmt.Errorf("%w: missing %s", ErrCorrupt, required)
		}
	}
	if _, modern := fields["executables"]; !modern {
		return State{}, fmt.Errorf("%w: missing executables", ErrCorrupt)
	}
	var integrationFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["integration"], &integrationFields); err != nil {
		return State{}, fmt.Errorf("%w: integration must be an object", ErrCorrupt)
	}
	for _, required := range []string{"executables", "desktop_entry", "desktop_sha256"} {
		if _, ok := integrationFields[required]; !ok {
			return State{}, fmt.Errorf("%w: integration missing %s", ErrCorrupt, required)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s State
	if err := dec.Decode(&s); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return State{}, fmt.Errorf("%w: trailing JSON", ErrCorrupt)
		}
		return State{}, fmt.Errorf("%w: trailing data: %v", ErrCorrupt, err)
	}
	if err := s.Validate(); err != nil {
		return State{}, err
	}
	return s, nil
}

func rejectDuplicateJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		return err
	}
	if err := consumeJSONValue(dec, first); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

func consumeJSONValue(dec *json.Decoder, token json.Token) error {
	delim, isDelim := token.(json.Delim)
	if !isDelim || delim != '{' && delim != '[' {
		return nil
	}
	if delim == '[' {
		for {
			t, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := t.(json.Delim); ok && d == ']' {
				return nil
			}
			if err := consumeJSONValue(dec, t); err != nil {
				return err
			}
		}
	}
	seen := make(map[string]struct{})
	for {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := t.(json.Delim); ok && d == '}' {
			return nil
		}
		key, ok := t.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate key %q", key)
		}
		seen[key] = struct{}{}
		value, err := dec.Token()
		if err != nil {
			return err
		}
		if err := consumeJSONValue(dec, value); err != nil {
			return err
		}
	}
}

func Load(path string) (State, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return State{}, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return State{}, fmt.Errorf("%w: state file is not a regular file", ErrCorrupt)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	return Decode(b)
}

func Write(path string, s State) error {
	_, err := write(path, s, syncDirectory)
	return err
}

func write(path string, s State, syncDir func(string) error) (bool, error) {
	if err := s.Validate(); err != nil {
		return false, err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return false, err
	}
	b = append(b, '\n')
	dir := filepath.Dir(path)
	if err := filesystem.SecureMkdirAll(dir, 0700); err != nil {
		return false, err
	}
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return false, filesystem.ErrSymlink
		}
		if !fi.Mode().IsRegular() {
			return false, fmt.Errorf("state path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return false, err
	}
	if err := syncDir(dir); err != nil {
		return true, err
	}
	return true, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func WriteForApp(l filesystem.Layout, s State) error {
	_, err := WriteForAppWithCommit(l, s)
	return err
}

// WriteForAppWithCommit reports whether the atomic rename completed even when
// the following directory durability flush fails.
func WriteForAppWithCommit(l filesystem.Layout, s State) (bool, error) {
	p, err := l.StatePath(s.App)
	if err != nil {
		return false, err
	}
	return write(p, s, syncDirectory)
}

func LoadForApp(l filesystem.Layout, appID string) (State, error) {
	p, err := l.StatePath(appID)
	if err != nil {
		return State{}, err
	}
	return Load(p)
}
