package state

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/manifest"
)

// fullValidState populates every optional field so each check is reachable.
func fullValidState() State {
	s := testState()
	s.Previous = "1.0.0"
	s.PreviousFingerprint = "sha256:" + strings.Repeat("b", 64)
	s.PreviousArtifact = "zip"
	s.PreviousChannel = "beta"
	s.DesktopEnabled = true
	s.Integration.DesktopEntry = "/tmp/applications/tarlink-demo.desktop"
	s.Integration.DesktopSHA256 = strings.Repeat("b", 64)
	s.Integration.IconFile = "/tmp/icons/512x512/apps/tarlink-demo.png"
	s.Integration.IconSHA256 = strings.Repeat("a", 64)
	s.Integration.IconSize = 512
	s.Integration.IconSource = ".tarlink-icon.png"
	s.Integration.PreviousIconFile = "/tmp/icons/256x256/apps/tarlink-demo.png"
	s.Integration.PreviousIconSHA256 = strings.Repeat("c", 64)
	s.Integration.PreviousIconSize = 256
	s.Integration.PreviousIconSource = "icon.png"
	return s
}

func clearPrevious(s *State) {
	s.Previous, s.PreviousFingerprint, s.PreviousArtifact, s.PreviousChannel, s.PreviousRuntime = "", "", "", "", nil
}

func clearIcon(s *State) {
	s.Integration.IconFile, s.Integration.IconSHA256, s.Integration.IconSource, s.Integration.IconSize = "", "", "", 0
}

func clearPreviousIcon(s *State) {
	s.Integration.PreviousIconFile, s.Integration.PreviousIconSHA256, s.Integration.PreviousIconSource, s.Integration.PreviousIconSize = "", "", "", 0
}

func TestValidateErrorsAndOrder(t *testing.T) {
	disabled := false
	cases := []struct {
		name string
		mut  func(*State)
		want string
	}{
		{"valid", func(s *State) {}, "<nil>"},
		{"authority", func(s *State) { s.Authority = "x" }, "corrupt state: unapproved package authority"},
		{"schema", func(s *State) { s.Schema = 2 }, "corrupt state: unsupported schema 2"},
		{"app", func(s *State) { s.App = "Bad/ID" }, "corrupt state: app: invalid application ID"},
		{"current version", func(s *State) { s.Current = "" }, "corrupt state: current version: invalid version"},
		{"current fingerprint", func(s *State) { s.CurrentFingerprint = "" }, "corrupt state: current fingerprint: must be a lowercase SHA-256 fingerprint"},
		{"previous version", func(s *State) { s.Previous = "bad/ver" }, "corrupt state: previous version: invalid version"},
		{"previous fingerprint", func(s *State) { s.PreviousFingerprint = "" }, "corrupt state: previous fingerprint: must be a lowercase SHA-256 fingerprint"},
		{"same identity", func(s *State) { s.Previous = s.Current; s.PreviousFingerprint = s.CurrentFingerprint }, "corrupt state: current and previous versions must differ"},
		{"same version different fp ok", func(s *State) { s.Previous = s.Current }, "<nil>"},
		{"previous fp requires previous", func(s *State) { clearPrevious(s); s.PreviousFingerprint = testFingerprint }, "corrupt state: previous fingerprint requires previous version"},
		{"channel empty", func(s *State) { s.Channel = "" }, "corrupt state: channel: must be a non-empty trimmed identifier"},
		{"previous channel", func(s *State) { s.PreviousChannel = "-x" }, "corrupt state: previous channel: must contain only lowercase letters, digits, '.', '_' or '-'"},
		{"previous channel requires previous", func(s *State) { clearPrevious(s); s.PreviousChannel = "beta" }, "corrupt state: previous channel requires previous version"},
		{"previous artifact requires previous", func(s *State) { clearPrevious(s); s.PreviousArtifact = "zip" }, "corrupt state: previous artifact requires previous version"},
		{"runtime", func(s *State) { s.Runtime = &manifest.Runtime{} }, "corrupt state: runtime: unsupported runtime schema 0"},
		{"previous runtime requires previous", func(s *State) { clearPrevious(s); s.PreviousRuntime = &manifest.Runtime{} }, "corrupt state: previous runtime requires previous version"},
		{"previous runtime", func(s *State) { s.PreviousRuntime = &manifest.Runtime{} }, "corrupt state: previous runtime: unsupported runtime schema 0"},
		{"previous artifact invalid", func(s *State) { s.PreviousArtifact = "rar" }, "corrupt state: previous artifact is invalid"},
		{"previous artifact empty", func(s *State) { s.PreviousArtifact = "" }, "corrupt state: previous artifact is invalid"},
		{"artifact", func(s *State) { s.Artifact = "rar" }, "corrupt state: unsupported artifact kind"},
		{"executables empty", func(s *State) { s.Executables = nil }, "corrupt state: executable list is empty"},
		{"executable name empty", func(s *State) { s.Executables[0].Name = "" }, "corrupt state: invalid or duplicate executable name"},
		{"executable name slash", func(s *State) { s.Executables[0].Name = "a/b" }, "corrupt state: invalid or duplicate executable name"},
		{"executable name duplicate", func(s *State) { s.Executables = append(s.Executables, s.Executables[0]) }, "corrupt state: invalid or duplicate executable name"},
		{"executable path", func(s *State) { s.Executables[0].Path = "/abs" }, "corrupt state: executable demo: must be a canonical relative path"},
		{"integration count", func(s *State) { s.Integration.Executables = nil }, "corrupt state: executable integration count mismatch"},
		{"integration no target", func(s *State) { s.Integration.Executables[0].Target = "" }, "corrupt state: executable integration is incomplete"},
		{"integration no name", func(s *State) { s.Integration.Executables[0].Name = "" }, "corrupt state: executable integration is incomplete"},
		{"integration no link", func(s *State) { s.Integration.Executables[0].Link = "" }, "corrupt state: executable integration is incomplete"},
		{"integration no link no binlink ok", func(s *State) {
			s.Integration.Executables[0].Link = ""
			s.Integration.Executables[0].CreateBinLink = &disabled
			s.Executables[0].CreateBinLink = &disabled
		}, "<nil>"},
		{"integration name mismatch", func(s *State) { s.Integration.Executables[0].Name = "other" }, "corrupt state: executable integration does not match executable state"},
		{"integration binlink mismatch", func(s *State) { s.Integration.Executables[0].CreateBinLink = &disabled }, "corrupt state: executable integration does not match executable state"},
		{"desktop entry relative", func(s *State) { s.Integration.DesktopEntry = "rel/x.desktop" }, "corrupt state: desktop entry must be absolute and clean"},
		{"desktop entry unclean", func(s *State) { s.Integration.DesktopEntry = "/tmp/../x.desktop" }, "corrupt state: desktop entry must be absolute and clean"},
		{"icon file relative", func(s *State) { s.Integration.IconFile = "icon.png" }, "corrupt state: icon file must be absolute and clean"},
		{"previous icon file relative", func(s *State) { s.Integration.PreviousIconFile = "../icon.svg" }, "corrupt state: previous icon file must be absolute and clean"},
		{"desktop entry required", func(s *State) { s.Integration.DesktopEntry = "" }, "corrupt state: desktop entry required when desktop integration is enabled"},
		{"desktop sha short", func(s *State) { s.Integration.DesktopSHA256 = "ab" }, "corrupt state: desktop ownership digest is invalid"},
		{"desktop sha upper", func(s *State) { s.Integration.DesktopSHA256 = strings.Repeat("B", 64) }, "corrupt state: desktop ownership digest is invalid"},
		{"desktop sha nonhex", func(s *State) { s.Integration.DesktopSHA256 = strings.Repeat("g", 64) }, "corrupt state: desktop ownership digest is invalid"},
		{"desktop disabled with fields", func(s *State) { s.DesktopEnabled = false }, "corrupt state: desktop ownership fields must be empty when desktop integration is disabled"},
		{"desktop disabled with sha only", func(s *State) {
			s.DesktopEnabled = false
			s.Integration.DesktopEntry = ""
			clearIcon(s)
			clearPreviousIcon(s)
		}, "corrupt state: desktop ownership fields must be empty when desktop integration is disabled"},
		{"icon sha short", func(s *State) { s.Integration.IconSHA256 = "" }, "corrupt state: icon ownership digest is invalid"},
		{"icon sha upper", func(s *State) { s.Integration.IconSHA256 = strings.Repeat("A", 64) }, "corrupt state: icon ownership digest is invalid"},
		{"icon sha nonhex", func(s *State) { s.Integration.IconSHA256 = strings.Repeat("z", 64) }, "corrupt state: icon ownership digest is invalid"},
		{"icon sha without file", func(s *State) { s.Integration.IconFile = "" }, "corrupt state: icon ownership fields are inconsistent"},
		{"icon source without file", func(s *State) { s.Integration.IconFile = ""; s.Integration.IconSHA256 = "" }, "corrupt state: icon source is inconsistent"},
		{"icon size negative", func(s *State) { s.Integration.IconSize = -1 }, "corrupt state: icon ownership size is invalid"},
		{"icon size without file", func(s *State) { clearIcon(s); s.Integration.IconSize = 48 }, "corrupt state: icon ownership fields are inconsistent"},
		{"icon size unsupported", func(s *State) { s.Integration.IconSize = 100 }, "corrupt state: icon ownership size is unsupported"},
		{"icon source invalid", func(s *State) { s.Integration.IconSource = "/abs.png" }, "corrupt state: icon source: must be a canonical relative path"},
		{"icon requires desktop", func(s *State) {
			s.DesktopEnabled = false
			s.Integration.DesktopEntry, s.Integration.DesktopSHA256 = "", ""
		}, "corrupt state: icon ownership fields require desktop integration"},
		{"previous icon sha without file", func(s *State) { s.Integration.PreviousIconFile = "" }, "corrupt state: previous icon ownership fields are inconsistent"},
		{"previous icon source without file", func(s *State) {
			s.Integration.PreviousIconFile, s.Integration.PreviousIconSHA256 = "", ""
		}, "corrupt state: previous icon ownership fields are inconsistent"},
		{"previous icon size negative", func(s *State) { s.Integration.PreviousIconSize = -1 }, "corrupt state: previous icon ownership size is invalid"},
		{"previous icon size without file", func(s *State) { clearPreviousIcon(s); s.Integration.PreviousIconSize = 48 }, "corrupt state: previous icon ownership fields are inconsistent"},
		{"previous icon size unsupported", func(s *State) { s.Integration.PreviousIconSize = 100 }, "corrupt state: previous icon ownership size is unsupported"},
		{"previous icon sha short", func(s *State) { s.Integration.PreviousIconSHA256 = "c" }, "corrupt state: previous icon ownership is invalid"},
		{"previous icon sha upper", func(s *State) { s.Integration.PreviousIconSHA256 = strings.Repeat("C", 64) }, "corrupt state: previous icon ownership is invalid"},
		{"previous icon requires desktop", func(s *State) {
			s.DesktopEnabled = false
			s.Integration.DesktopEntry, s.Integration.DesktopSHA256 = "", ""
			clearIcon(s)
		}, "corrupt state: previous icon ownership is invalid"},
		{"previous icon source invalid", func(s *State) { s.Integration.PreviousIconSource = "" }, "corrupt state: previous icon source: must be a canonical relative path"},
		// Several invalid fields pin which error Validate reports first.
		{"order authority schema", func(s *State) { s.Authority = "x"; s.Schema = 9 }, "corrupt state: unapproved package authority"},
		{"order schema app", func(s *State) { s.Schema = 9; s.App = "" }, "corrupt state: unsupported schema 9"},
		{"order app current", func(s *State) { s.App = ""; s.Current = "" }, "corrupt state: app: invalid application ID"},
		{"order fp previous", func(s *State) { s.CurrentFingerprint = ""; s.Previous = "bad/ver" }, "corrupt state: current fingerprint: must be a lowercase SHA-256 fingerprint"},
		{"order previous channel", func(s *State) { s.PreviousFingerprint = ""; s.Channel = "" }, "corrupt state: previous fingerprint: must be a lowercase SHA-256 fingerprint"},
		{"order channel artifact", func(s *State) { s.Channel = ""; s.Artifact = "" }, "corrupt state: channel: must be a non-empty trimmed identifier"},
		{"order prevchannel-req prevartifact-req", func(s *State) {
			clearPrevious(s)
			s.PreviousChannel, s.PreviousArtifact = "beta", "zip"
		}, "corrupt state: previous channel requires previous version"},
		{"order runtime prevruntime-req", func(s *State) {
			clearPrevious(s)
			s.Runtime, s.PreviousRuntime = &manifest.Runtime{}, &manifest.Runtime{}
		}, "corrupt state: runtime: unsupported runtime schema 0"},
		{"order prevartifact-req runtime", func(s *State) { clearPrevious(s); s.PreviousArtifact = "zip"; s.Runtime = &manifest.Runtime{} }, "corrupt state: previous artifact requires previous version"},
		{"order prevruntime prevartifact", func(s *State) { s.PreviousRuntime = &manifest.Runtime{}; s.PreviousArtifact = "rar" }, "corrupt state: previous runtime: unsupported runtime schema 0"},
		{"order artifact executables", func(s *State) { s.Artifact = ""; s.Executables = nil }, "corrupt state: unsupported artifact kind"},
		{"order exec path integration", func(s *State) { s.Executables[0].Path = ""; s.Integration.Executables = nil }, "corrupt state: executable demo: must be a canonical relative path"},
		{"order integration desktop path", func(s *State) { s.Integration.Executables[0].Target = ""; s.Integration.DesktopEntry = "rel" }, "corrupt state: executable integration is incomplete"},
		{"order desktop path desktop sha", func(s *State) { s.Integration.IconFile = "rel"; s.Integration.DesktopSHA256 = "" }, "corrupt state: icon file must be absolute and clean"},
		{"order desktop sha icon sha", func(s *State) { s.Integration.DesktopSHA256 = ""; s.Integration.IconSHA256 = "" }, "corrupt state: desktop ownership digest is invalid"},
		{"order icon size prev icon size", func(s *State) { s.Integration.IconSize = -1; s.Integration.PreviousIconSize = -1 }, "corrupt state: icon ownership size is invalid"},
		{"order icon source prev icon", func(s *State) { s.Integration.IconSource = "/x"; s.Integration.PreviousIconSource = "" }, "corrupt state: icon source: must be a canonical relative path"},
		{"order prev size prev sha", func(s *State) { s.Integration.PreviousIconSize = 100; s.Integration.PreviousIconSHA256 = "" }, "corrupt state: previous icon ownership size is unsupported"},
		{"order icon sha icon requires desktop", func(s *State) {
			s.DesktopEnabled = false
			s.Integration.DesktopEntry, s.Integration.DesktopSHA256 = "", ""
			s.Integration.IconSHA256 = ""
		}, "corrupt state: icon ownership digest is invalid"},
		{"order desktop entry req desktop sha", func(s *State) { s.Integration.DesktopEntry = ""; s.Integration.DesktopSHA256 = "" }, "corrupt state: desktop entry required when desktop integration is enabled"},
		{"zero state", func(s *State) { *s = State{} }, "corrupt state: unsupported schema 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := fullValidState()
			tc.mut(&value)
			err := value.Validate()
			if err != nil && !errors.Is(err, ErrCorrupt) {
				t.Fatalf("error is not ErrCorrupt: %v", err)
			}
			if got := fmt.Sprint(err); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
