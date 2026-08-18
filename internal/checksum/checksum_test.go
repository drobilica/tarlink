package checksum

import (
	"strings"
	"testing"
)

func TestNewHasherAcceptsSupportedDigests(t *testing.T) {
	for _, test := range []struct {
		algorithm string
		digest    string
	}{
		{algorithm: "sha256", digest: strings.Repeat("0", 64)},
		{algorithm: "sha512", digest: strings.Repeat("0", 128)},
	} {
		if _, err := NewHasher(test.algorithm, test.digest); err != nil {
			t.Errorf("NewHasher(%q) error = %v", test.algorithm, err)
		}
	}
}

func TestNewHasherRejectsInvalidAlgorithmsAndDigests(t *testing.T) {
	for _, test := range []struct {
		algorithm string
		digest    string
	}{
		{algorithm: "md5", digest: strings.Repeat("0", 32)},
		{algorithm: "sha1", digest: strings.Repeat("0", 40)},
		{algorithm: "sha256", digest: strings.Repeat("0", 63)},
		{algorithm: "sha512", digest: strings.Repeat("0", 127)},
		{algorithm: "sha256", digest: strings.Repeat("A", 64)},
		{algorithm: "sha512", digest: strings.Repeat("g", 128)},
	} {
		if _, err := NewHasher(test.algorithm, test.digest); err == nil {
			t.Errorf("NewHasher(%q, %q) unexpectedly succeeded", test.algorithm, test.digest)
		}
	}
}
