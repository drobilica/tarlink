package app

import (
	"strings"
	"testing"
)

const lockTestFingerprint = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestParseLockIsStrict(t *testing.T) {
	valid := "schema: 1\nplatform: linux/amd64\napplications:\n  - id: alpha\n    channel: stable\n    version: \"1.0\"\n    fingerprint: " + lockTestFingerprint + "\n"
	lock, err := ParseLock([]byte(valid))
	if err != nil || len(lock.Applications) != 1 || lock.Applications[0].ID != "alpha" {
		t.Fatalf("ParseLock(valid) = %+v, %v", lock, err)
	}
	for _, value := range []string{
		strings.Replace(valid, "schema: 1", "schema: \"1\"", 1),
		strings.Replace(valid, "platform: linux/amd64", "platform: linux/amd64\nextra: nope", 1),
		strings.Replace(valid, "id: alpha", "id: alpha\n    id: beta", 1),
		strings.Replace(valid, "platform: linux/amd64", "platform: darwin/amd64", 1),
		strings.Replace(valid, "version: \"1.0\"", "version: 1", 1),
	} {
		if _, err := ParseLock([]byte(value)); err == nil {
			t.Fatalf("ParseLock accepted invalid lock:\n%s", value)
		}
	}
}

func TestLockValidationRequiresSortedApplications(t *testing.T) {
	lock := Lockfile{Schema: LockSchema, Platform: LockPlatformAMD, Applications: []LockEntry{
		{ID: "beta", Channel: "stable", Version: "1", Fingerprint: lockTestFingerprint},
		{ID: "alpha", Channel: "stable", Version: "1", Fingerprint: lockTestFingerprint},
	}}
	if err := lock.Validate(); err == nil {
		t.Fatal("Validate accepted unsorted applications")
	}
}
