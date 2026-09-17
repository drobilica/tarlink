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
	Home       string
	DataHome   string
	StateHome  string
	CacheHome  string
	ConfigHome string

	Apps string
	// Runtimes contains immutable dependency deployments. It is deliberately
	// separate from application payloads: an application package only retains a
	// reference to a runtime deployment, never a copied runtime tree.
	Runtimes         string
	States           string
	Cache            string
	RepositoryConfig string
	Locks            string
	Bin              string
	Desktop          string
	Icons            string
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
	if !validLayoutPath(home) || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return Layout{}, errors.New("home must be an absolute, clean path")
	}
	resolve := func(name, fallback string) (string, error) {
		value := getenv(name)
		if value == "" {
			value = fallback
		}
		if !validLayoutPath(value) || !filepath.IsAbs(value) || filepath.Clean(value) != value || !contained(home, value) {
			return "", fmt.Errorf("%s must be an absolute, clean path within home", name)
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
	config, err := resolve("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err != nil {
		return Layout{}, err
	}

	l := Layout{Home: home, DataHome: data, StateHome: state, CacheHome: cache, ConfigHome: config}
	l.Apps = filepath.Join(data, productDir, "apps")
	l.Runtimes = filepath.Join(data, productDir, "runtimes")
	l.States = filepath.Join(state, productDir, "states")
	l.Cache = filepath.Join(cache, productDir)
	l.RepositoryConfig = filepath.Join(config, productDir, "repositories.json")
	l.Locks = filepath.Join(state, productDir, "locks")
	l.Bin = filepath.Join(home, ".local", "bin")
	l.Desktop = filepath.Join(data, "applications")
	l.Icons = filepath.Join(data, "icons", "hicolor")
	return l, nil
}

func validLayoutPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

// Ensure creates TarLink's private directories. Integration directories are
// also created because they are user-owned and are part of the layout.
func (l Layout) Ensure() error {
	dirs := []string{l.Apps, l.Runtimes, l.States, l.Cache, l.Locks}
	if l.RepositoryConfig != "" {
		dirs = append(dirs, filepath.Dir(l.RepositoryConfig))
	}
	for _, dir := range dirs {
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

// RuntimePath returns the immutable deployment directory for an exact runtime
// identity. Runtime deployments never have a mutable "current" pointer.
func (l Layout) RuntimePath(id, version, fingerprint string) (string, error) {
	if err := ValidateID(id); err != nil {
		return "", err
	}
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	if _, err := l.PackagePath(id, version, fingerprint); err != nil {
		return "", err
	}
	return filepath.Join(l.Runtimes, id, version, ".tarlink-runtime-"+fingerprint[len("sha256:"):]), nil
}

// StatePath returns the state file for an application ID.
func (l Layout) StatePath(appID string) (string, error) {
	if err := ValidateID(appID); err != nil {
		return "", err
	}
	return filepath.Join(l.States, appID+".json"), nil
}

// AppPath returns the version root for an application. A version root is not
// itself an installed package; PackagePath returns the package directory.
func (l Layout) AppPath(appID, version string) (string, error) {
	if err := ValidateID(appID); err != nil {
		return "", err
	}
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	return filepath.Join(l.Apps, appID, version), nil
}

// PackagePath returns the canonical internal path for one TarLink package
// identity. Fingerprinted packages live below their version directory, which
// keeps the identity separate from any valid upstream version string.
func (l Layout) PackagePath(appID, version, fingerprint string) (string, error) {
	if err := ValidateID(appID); err != nil {
		return "", err
	}
	if err := ValidateVersion(version); err != nil {
		return "", err
	}
	const prefix = "sha256:"
	if !strings.HasPrefix(fingerprint, prefix) || len(fingerprint) != len(prefix)+64 || strings.ToLower(fingerprint) != fingerprint {
		return "", errors.New("package fingerprint must be a lowercase SHA-256 digest")
	}
	for _, r := range fingerprint[len(prefix):] {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "", errors.New("package fingerprint must be a lowercase SHA-256 digest")
		}
	}
	return filepath.Join(l.Apps, appID, version, fmt.Sprintf(".tarlink-package-%s", fingerprint)), nil
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
