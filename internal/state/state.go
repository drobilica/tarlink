// Package state persists TarLink's schema-1 per-application state record.
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
)

const Schema = 1

type Integration struct {
	ExecutableLink     string `json:"executable_link"`
	ExecutableTarget   string `json:"executable_target"`
	DesktopEntry       string `json:"desktop_entry"`
	DesktopSHA256      string `json:"desktop_sha256"`
	IconFile           string `json:"icon_file,omitempty"`
	IconSHA256         string `json:"icon_sha256,omitempty"`
	IconSource         string `json:"icon_source,omitempty"`
	PreviousIconFile   string `json:"previous_icon_file,omitempty"`
	PreviousIconSHA256 string `json:"previous_icon_sha256,omitempty"`
	PreviousIconSource string `json:"previous_icon_source,omitempty"`
}

type State struct {
	Schema         int         `json:"schema"`
	App            string      `json:"app"`
	Current        string      `json:"current"`
	Previous       string      `json:"previous,omitempty"`
	Executable     string      `json:"executable"`
	DesktopEnabled bool        `json:"desktop_enabled"`
	Integration    Integration `json:"integration"`
}

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
	if s.Previous != "" {
		if err := filesystem.ValidateVersion(s.Previous); err != nil {
			return fmt.Errorf("%w: previous version: %v", ErrCorrupt, err)
		}
		if s.Previous == s.Current {
			return fmt.Errorf("%w: current and previous versions must differ", ErrCorrupt)
		}
	}
	if err := validateExecutable(s.Executable); err != nil {
		return fmt.Errorf("%w: executable: %v", ErrCorrupt, err)
	}
	for name, path := range map[string]string{
		"executable link":    s.Integration.ExecutableLink,
		"executable target":  s.Integration.ExecutableTarget,
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
	if s.Integration.ExecutableLink == "" || s.Integration.ExecutableTarget == "" {
		return fmt.Errorf("%w: executable integration paths are required", ErrCorrupt)
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

func (s State) ValidateForLayout(l filesystem.Layout) error {
	if err := s.Validate(); err != nil {
		return err
	}
	appRoot := filepath.Join(l.Apps, s.App)
	expectedLink := filepath.Join(l.Bin, s.App)
	expectedTarget := filepath.Join(appRoot, "current", filepath.FromSlash(s.Executable))
	if s.Integration.ExecutableLink != expectedLink || s.Integration.ExecutableTarget != expectedTarget {
		return fmt.Errorf("%w: integration paths do not match the canonical layout", ErrCorrupt)
	}
	expectedDesktop := ""
	if s.DesktopEnabled {
		expectedDesktop = filepath.Join(l.Desktop, "tarlink-"+s.App+".desktop")
	}
	expectedIcon := ""
	if s.Integration.IconFile != "" {
		expectedIcon = filepath.Join(l.Icons, iconSizeDirectory(s.Integration.IconFile), "apps", "tarlink-"+s.App+filepath.Ext(s.Integration.IconFile))
	}
	if s.Integration.IconFile != expectedIcon {
		return fmt.Errorf("%w: icon path does not match the canonical layout", ErrCorrupt)
	}
	expectedPreviousIcon := ""
	if s.Integration.PreviousIconFile != "" {
		expectedPreviousIcon = filepath.Join(l.Icons, iconSizeDirectory(s.Integration.PreviousIconFile), "apps", "tarlink-"+s.App+filepath.Ext(s.Integration.PreviousIconFile))
	}
	if s.Integration.PreviousIconFile != expectedPreviousIcon {
		return fmt.Errorf("%w: previous icon path does not match the canonical layout", ErrCorrupt)
	}
	if s.Integration.DesktopEntry != expectedDesktop {
		return fmt.Errorf("%w: desktop path does not match the canonical layout", ErrCorrupt)
	}
	return nil
}

func iconSizeDirectory(value string) string {
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
	for _, required := range []string{"schema", "app", "current", "executable", "desktop_enabled", "integration"} {
		if _, ok := fields[required]; !ok {
			return State{}, fmt.Errorf("%w: missing %s", ErrCorrupt, required)
		}
	}
	var integrationFields map[string]json.RawMessage
	if err := json.Unmarshal(fields["integration"], &integrationFields); err != nil {
		return State{}, fmt.Errorf("%w: integration must be an object", ErrCorrupt)
	}
	for _, required := range []string{"executable_link", "executable_target", "desktop_entry", "desktop_sha256"} {
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
