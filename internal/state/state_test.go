package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testState() State {
	return State{Schema: Schema, App: "demo", Current: "1.2.3", Executable: "bin/demo", Integration: Integration{ExecutableLink: "/tmp/bin/demo", ExecutableTarget: "/tmp/apps/demo/current/bin/demo"}}
}

func TestDecodeStrictAndTrailing(t *testing.T) {
	b := []byte(`{"schema":1,"app":"demo","current":"1.2.3","executable":"bin/demo","desktop_enabled":false,"integration":{"executable_link":"/tmp/bin/demo","executable_target":"/tmp/apps/demo/current/bin/demo","desktop_entry":"","desktop_sha256":""}}`)
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
	if err := os.WriteFile(p, []byte(`{"schema":1,"app":"demo","current":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err=%v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != `{"schema":1,"app":"demo","current":""}` {
		t.Fatal("corrupt file changed")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add([]byte(`{"schema":1,"app":"demo","current":"1","executable":"x","desktop_enabled":false,"integration":{"executable_link":"/tmp/x","executable_target":"/tmp/y","desktop_entry":"","desktop_sha256":""}}`))
	f.Add([]byte("not json"))
	f.Fuzz(func(t *testing.T, data []byte) { _, _ = Decode(data) })
}
