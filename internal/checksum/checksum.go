// Package checksum defines the artifact digest algorithms accepted by TarLink.
package checksum

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
)

type definition struct {
	size int
	new  func() hash.Hash
}

var definitions = map[string]definition{
	"sha256": {size: sha256.Size, new: sha256.New},
	"sha512": {size: sha512.Size, new: sha512.New},
}

// NewHasher validates algorithm and digest, then returns a hash for verifying
// the corresponding artifact bytes.
func NewHasher(algorithm, digest string) (hash.Hash, error) {
	definition, ok := definitions[algorithm]
	if !ok {
		return nil, fmt.Errorf("unsupported artifact verification algorithm %q", algorithm)
	}
	if len(digest) != definition.size*2 || digest != strings.ToLower(digest) {
		return nil, fmt.Errorf("artifact verification digest must be exactly %d lowercase hexadecimal characters", definition.size*2)
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != definition.size {
		return nil, fmt.Errorf("artifact verification digest must be exactly %d lowercase hexadecimal characters", definition.size*2)
	}
	return definition.new(), nil
}

// Validate checks an artifact verification algorithm and digest.
func Validate(algorithm, digest string) error {
	_, err := NewHasher(algorithm, digest)
	return err
}
