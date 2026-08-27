package app

import (
	"context"
	"testing"
)

func TestCheckRegistryRejectsAmbiguousSelectors(t *testing.T) {
	_, err := (&Core{}).CheckRegistry(context.Background(), RegistryCheckOptions{
		Root: "/registry", App: "fixture", AllArtifacts: true,
	})
	if err == nil {
		t.Fatal("ambiguous registry check selectors unexpectedly accepted")
	}
}

func TestCheckRegistryRequiresRoot(t *testing.T) {
	_, err := (&Core{}).CheckRegistry(context.Background(), RegistryCheckOptions{})
	if err == nil {
		t.Fatal("registry check without a root unexpectedly accepted")
	}
}
