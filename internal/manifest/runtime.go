package manifest

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v3"
)

// ParseRuntime reads the separate, deliberately small runtime-manifest
// language. Runtime manifests contain deployment identity only; they cannot
// describe commands, paths, environment, mounts or hooks.
func ParseRuntime(r io.Reader) (*Runtime, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read runtime manifest: %w", err)
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("runtime manifest exceeds %d bytes", MaxManifestBytes)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("decode runtime YAML: %w", err)
	}
	if err := validateYAMLNode(&document); err != nil {
		return nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, errors.New("runtime manifest must contain one mapping document")
	}
	root, err := requiredMapping(document.Content[0], "runtime manifest", []string{"schema", "id", "kind", "version", "platform", "artifact", "interface"}, nil)
	if err != nil {
		return nil, err
	}
	artifact, err := requiredMapping(root["artifact"], "runtime artifact", []string{"url", "archive", "verification"}, nil)
	if err != nil {
		return nil, err
	}
	if _, err := requiredMapping(artifact["verification"], "runtime artifact.verification", []string{"algorithm", "digest", "source"}, nil); err != nil {
		return nil, err
	}
	var decoded struct {
		Schema    int             `yaml:"schema"`
		ID        string          `yaml:"id"`
		Kind      string          `yaml:"kind"`
		Version   string          `yaml:"version"`
		Platform  string          `yaml:"platform"`
		Artifact  RuntimeArtifact `yaml:"artifact"`
		Interface string          `yaml:"interface"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode runtime manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("runtime manifest must contain exactly one YAML document")
	}
	platform, ok := ParsePlatformKey(decoded.Platform)
	if !ok {
		return nil, fmt.Errorf("unsupported runtime platform %q", decoded.Platform)
	}
	runtime := &Runtime{Schema: decoded.Schema, ID: decoded.ID, Kind: decoded.Kind, Version: decoded.Version, Platform: platform, Artifact: decoded.Artifact, Interface: decoded.Interface}
	if err := runtime.Validate(); err != nil {
		return nil, err
	}
	return runtime, nil
}

func ParseRuntimeBytes(data []byte) (*Runtime, error) { return ParseRuntime(bytes.NewReader(data)) }

// Fingerprint is the immutable deployment identity, including its fixed
// backend contract. It intentionally omits informational verification source.
func (r Runtime) Fingerprint() (string, error) {
	if err := r.Validate(); err != nil {
		return "", err
	}
	var encoded bytes.Buffer
	encoded.WriteString("tarlink/runtime/v1\x00")
	writeFingerprintInt(&encoded, uint64(r.Schema))
	for _, value := range []string{r.ID, r.Kind, r.Version, r.Platform.OS, r.Platform.Arch, r.Artifact.URL, r.Artifact.Archive, r.Artifact.Verification.Algorithm, r.Artifact.Verification.Digest, r.Interface} {
		writeFingerprintString(&encoded, value)
	}
	return "sha256:" + fmt.Sprintf("%x", sha256.Sum256(encoded.Bytes())), nil
}
