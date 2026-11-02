package app

import (
	"strings"
	"testing"
)

func TestReviewedRegistryAssetChoices(t *testing.T) {
	for _, tc := range []struct{ app, format, good, excluded string }{
		{"diddy-kong-racing-recompiled", "appimage", "DKR-R-1.0.12-Linux-x86_64.AppImage", "DKR-R-1.0.12-SteamDeck-x86_64.AppImage"},
		{"rage-racer-pc", "zip", "Rage-Racer-0.6.13-alpha-linux-x86_64.zip", "Rage-Racer-Editor-0.6.13-alpha-linux-x86_64.zip"},
		{"sonic-3-recompiled", "appimage", "Sonic3Recomp-linux-x86_64-v0.6.3.AppImage", "Sonic3KRecomp-linux-x86_64-v0.6.3.AppImage"},
		{"xemu", "appimage", "xemu-0.8.139-x86_64.AppImage", "xemu-0.8.139-dbg-x86_64.AppImage"},
	} {
		t.Run(tc.app, func(t *testing.T) {
			result := assessRegistryAssets(tc.app, "linux-amd64", tc.format, []string{tc.good, tc.excluded})
			if result.Status != "matched" || len(result.Matches) != 1 || result.Matches[0] != tc.good || len(result.Rejected) != 1 || !strings.Contains(result.Rejected[0].Reason, "reviewed") {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestRegistryAssetDiagnostics(t *testing.T) {
	names := []string{"app.AppImage", "app-arm64.AppImage", "app-linux-amd64.zip"}
	result := assessRegistryAssets("godot", "linux-amd64", "appimage", names)
	if result.Status != "missing" || len(result.Rejected) != 3 {
		t.Fatalf("%+v", result)
	}
	detail := describeRegistryAssets(result)
	for _, name := range names {
		if !strings.Contains(detail, name) {
			t.Fatal(detail)
		}
	}
	names = []string{"a-amd64.AppImage", "b-amd64.AppImage"}
	result = assessRegistryAssets("godot", "linux-amd64", "appimage", names)
	if result.Status != "ambiguous" || len(result.Matches) != 2 {
		t.Fatalf("%+v", result)
	}
}
