package app

import "testing"

func TestEnvironmentPolicy(t *testing.T) {
	for _, test := range []struct {
		name         string
		goos, goarch string
		euid         int
		want         ErrorCode
	}{
		{name: "supported", goos: "linux", goarch: "amd64", euid: 1000},
		{name: "root", goos: "linux", goarch: "amd64", euid: 0, want: CodeRoot},
		{name: "arm64", goos: "linux", goarch: "arm64", euid: 1000},
		{name: "unsupported architecture", goos: "linux", goarch: "riscv64", euid: 1000, want: CodeUnsupportedPlatform},
		{name: "macOS", goos: "darwin", goarch: "amd64", euid: 1000, want: CodeUnsupportedPlatform},
		{name: "Windows", goos: "windows", goarch: "amd64", euid: 1000, want: CodeUnsupportedPlatform},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := checkEnvironment(test.goos, test.goarch, test.euid)
			if code := CodeOf(err); code != test.want {
				t.Fatalf("error code = %q, want %q (error %v)", code, test.want, err)
			}
		})
	}
}
