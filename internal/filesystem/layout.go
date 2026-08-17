// Package filesystem contains the rootless, user-owned TarLink filesystem layout.
package filesystem

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const productDir = "tarlink"

// Layout is the complete set of directories TarLink may use. All paths are
// absolute and are below the current user's home directory (or an XDG home).
type Layout struct {
	Home      string
	DataHome  string
	StateHome string
	CacheHome string

	Apps    string
	States  string
	Cache   string
	Locks   string
	Bin     string
	Desktop string
}

// NewLayout resolves the layout for the current user.
func NewLayout() (Layout, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Layout{}, fmt.Errorf("user home: %w", err)
	}
	return LayoutFor(home, os.Getenv)
}

// LayoutFor resolves a layout using home and an environment lookup function.
// It is intentionally exported so callers and tests can provide a temporary
// home without changing process-global environment variables.
func LayoutFor(home string, getenv func(string) string) (Layout, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return Layout{}, errors.New("home must be an absolute, clean path")
	}
	resolve := func(name, fallback string) (string, error) {
		value := getenv(name)
		if value == "" {
			value = fallback
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return "", fmt.Errorf("%s must be an absolute, clean path", name)
		}
		return value, nil
	}
	data, err := resolve("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if err != nil {
		return Layout{}, err
	}
	state, err := resolve("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	if err != nil {
		return Layout{}, err
	}
	cache, err := resolve("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err != nil {
		return Layout{}, err
	}

	l := Layout{Home: home, DataHome: data, StateHome: state, CacheHome: cache}
	l.Apps = filepath.Join(data, productDir, "apps")
	l.States = filepath.Join(state, productDir, "states")
	l.Cache = filepath.Join(cache, productDir)
	l.Locks = filepath.Join(state, productDir, "locks")
	l.Bin = filepath.Join(home, ".local", "bin")
	l.Desktop = filepath.Join(data, "applications")
	return l, nil
}

// Ensure creates TarLink's private directories. Integration directories are
// also created because they are user-owned and are part of the layout.
func (l Layout) Ensure() error {
	for _, dir := range []string{l.Apps, l.States, l.Cache, l.Locks} {
		if err := SecureMkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	for _, dir := range []string{l.Bin, l.Desktop} {
		if err := SecureMkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// StatePath returns the state file for an application ID.
func (l Layout) StatePath(appID string) (string, error) {
	if err := ValidateID(appID); err != nil {
		return "", err
	}
	return filepath.Join(l.States, appID+".json"), nil
}

// AppPath returns the installation directory for an application/version.
func (l Layout) AppPath(appID, version string) (string, error) {
	if err := ValidateID(appID); err != nil {
		return "", err
	}
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	return filepath.Join(l.Apps, appID, version), nil
}

// ValidateID validates an application ID as a safe single path component.
func ValidateID(id string) error { return validateComponent("application ID", id, true) }

// ValidateVersion validates a version as a safe single path component.
func ValidateVersion(version string) error { return validateComponent("version", version, false) }

func validateComponent(label, value string, id bool) error {
	max := 128
	if id {
		max = 80
	}
	if value == "" || len(value) > max || !utf8.ValidString(value) || strings.TrimSpace(value) != value ||
		strings.IndexByte(value, 0) >= 0 || value == "." || value == ".." || filepath.IsAbs(value) ||
		strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("invalid %s", label)
	}
	if id {
		for _, r := range value {
			if !(r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				return fmt.Errorf("invalid %s", label)
			}
		}
		if !((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= '0' && value[0] <= '9')) {
			return fmt.Errorf("invalid %s", label)
		}
	} else {
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("invalid %s", label)
			}
		}
	}
	return nil
}
