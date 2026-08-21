package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drobilica/tarlink/internal/filesystem"
)

func testState() State {
	return State{Schema: Schema, App: "demo", Current: "1.2.3", Channel: "stable", Pinned: false, Artifact: "tar.gz", Executables: []Executable{{Name: "demo", Path: "bin/demo"}}, Integration: Integration{Executables: []ExecutableIntegration{{Name: "demo", Path: "bin/demo", Link: "/tmp/bin/demo", Target: "/tmp/apps/demo/current/bin/demo"}}}}
}

func TestDecodeStrictAndTrailing(t *testing.T) {
	b := []byte(`{"schema":2,"app":"demo","current":"1.2.3","channel":"stable","pinned":false,"artifact":"tar.gz","executables":[{"name":"demo","path":"bin/demo"}],"desktop_enabled":false,"integration":{"executables":[{"name":"demo","path":"bin/demo","link":"/tmp/bin/demo","target":"/tmp/apps/demo/current/bin/demo"}],"desktop_entry":"","desktop_sha256":""}}`)
	if _, err := Decode(b); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(append(b, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON accepted")
	}
	if _, err := Decode([]byte(strings.Replace(string(b), `"app"`, `"extra":1,"app"`, 1))); err == nil {
		t.Fatal("unknown field accepted")
	}
	dup := strings.Replace(string(b), `"app":"demo"`, `"app":"demo","app":"demo"`, 1)
	if _, err := Decode([]byte(dup)); err == nil {
		t.Fatal("duplicate field accepted")
	}
}

func TestWriteLoadAndCorruptPreserved(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "state.json")
	if err := Write(p, testState()); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got.App != "demo" {
		t.Fatalf("state=%+v err=%v", got, err)
	}
	if err := os.WriteFile(p, []byte(`{"schema":2,"app":"demo","current":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err=%v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"schema":2,"app":"demo","current":""}` {
		t.Fatal("corrupt file changed")
	}
}

func TestWriteReportsPostRenameSyncFailureAsCommitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	injected := errors.New("injected directory sync failure")
	committed, err := write(path, testState(), func(string) error { return injected })
	if !committed || !errors.Is(err, injected) {
		t.Fatalf("committed=%t error=%v", committed, err)
	}
	loaded, loadErr := Load(path)
	if loadErr != nil || loaded.Current != testState().Current {
		t.Fatalf("state=%#v error=%v", loaded, loadErr)
	}
}

func TestValidateForLayoutRequiresCanonicalOwnedPaths(t *testing.T) {
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, func(name string) string {
		switch name {
		case "XDG_DATA_HOME":
			return filepath.Join(home, "data")
		case "XDG_STATE_HOME":
			return filepath.Join(home, "state")
		case "XDG_CACHE_HOME":
			return filepath.Join(home, "cache")
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	value := State{
		Schema: Schema, App: "demo", Current: "1.2.3", Channel: "stable", Artifact: "tar.gz", Executables: []Executable{{Name: "demo", Path: "bin/demo"}},
		Integration: Integration{
			Executables: []ExecutableIntegration{{Name: "demo", Path: "bin/demo", Link: filepath.Join(layout.Bin, "demo"), Target: filepath.Join(layout.Apps, "demo", "current", "bin", "demo")}},
		},
	}
	if err := value.ValidateForLayout(layout); err != nil {
		t.Fatalf("canonical state rejected: %v", err)
	}
	value.Integration.Executables[0].Link = filepath.Join(home, "unrelated", "demo")
	if err := value.ValidateForLayout(layout); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unrelated integration accepted: %v", err)
	}
}

func TestValidateForLayoutRequiresCanonicalIconOwnership(t *testing.T) {
	home := t.TempDir()
	layout, err := filesystem.LayoutFor(home, nil)
	if err != nil {
		t.Fatal(err)
	}
	value := testState()
	value.Integration.Executables[0].Link = filepath.Join(layout.Bin, "demo")
	value.Integration.Executables[0].Target = filepath.Join(layout.Apps, "demo", "current", "bin", "demo")
	value.DesktopEnabled = true
	value.Integration.DesktopEntry = filepath.Join(layout.Desktop, "tarlink-demo.desktop")
	value.Integration.DesktopSHA256 = strings.Repeat("b", 64)
	value.Integration.IconFile = filepath.Join(layout.Icons, "48x48", "apps", "tarlink-demo.png")
	value.Integration.IconSHA256 = strings.Repeat("a", 64)
	if err := value.ValidateForLayout(layout); err != nil {
		t.Fatalf("canonical icon rejected: %v", err)
	}
	value.Integration.IconFile = filepath.Join(home, "elsewhere", "icon.png")
	if !errors.Is(value.ValidateForLayout(layout), ErrCorrupt) {
		t.Fatal("noncanonical icon accepted")
	}
}

func TestValidateRejectsMalformedPreviousIconPath(t *testing.T) {
	value := testState()
	value.DesktopEnabled = true
	value.Integration.DesktopEntry = "/tmp/applications/tarlink-demo.desktop"
	value.Integration.DesktopSHA256 = strings.Repeat("b", 64)
	value.Integration.PreviousIconFile = "../icon.svg"
	value.Integration.PreviousIconSHA256 = strings.Repeat("a", 64)
	value.Integration.PreviousIconSource = "icon.svg"
	if !errors.Is(value.Validate(), ErrCorrupt) {
		t.Fatal("malformed previous icon path accepted")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"schema":2,"app":"demo","current":"1","channel":"stable","pinned":false,"executable":"x","desktop_enabled":false,"integration":{"executable_link":"/tmp/x","executable_target":"/tmp/y","desktop_entry":"","desktop_sha256":""}}`))
	f.Add([]byte("not json"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Decode(data) })
}
