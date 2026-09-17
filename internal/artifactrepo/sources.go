package artifactrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/locking"
)

type sourceFile struct {
	Sources []string `json:"sources"`
}

func NormalizeSource(value, cwd string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("repository source is empty")
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Scheme != "" {
		if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" {
			return "", errors.New("repository source URL must be HTTPS")
		}
		return parsed.String(), nil
	}
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	path, err := filepath.Abs(filepath.Join(cwd, value))
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}

func LoadSources(path string) ([]string, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("repository sources must be a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var value sourceFile
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("read repository sources: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("read repository sources: expected one JSON object")
	}
	return append([]string(nil), value.Sources...), nil
}

func SaveSources(path string, sources []string) error {
	if !filepath.IsAbs(path) {
		return errors.New("repository config path must be absolute")
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sourceFile{Sources: sources}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0600)
}

func AddSource(path, value string) error {
	lock, err := locking.AcquireWithTimeout(context.Background(), path+".lock", locking.DefaultTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	sources, err := LoadSources(path)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if source == value {
			return nil
		}
	}
	return SaveSources(path, append(sources, value))
}
func RemoveSource(path, value string) error {
	lock, err := locking.AcquireWithTimeout(context.Background(), path+".lock", locking.DefaultTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	sources, err := LoadSources(path)
	if err != nil {
		return err
	}
	result := sources[:0]
	for _, source := range sources {
		if source != value {
			result = append(result, source)
		}
	}
	return SaveSources(path, result)
}
