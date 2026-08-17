package app

import (
	"errors"
	"runtime"
)

func CheckEnvironment() error {
	return checkEnvironment(runtime.GOOS, runtime.GOARCH, effectiveUID())
}

func checkEnvironment(goos, goarch string, euid int) error {
	if goos != "linux" || goarch != "amd64" && goarch != "arm64" {
		return &Error{Code: CodeUnsupportedPlatform, Op: "startup", Err: errors.New("TarLink supports Linux amd64 and arm64 only")}
	}
	if euid == 0 {
		return &Error{Code: CodeRoot, Op: "startup", Err: errors.New("TarLink refuses to run as root; run it as your normal user")}
	}
	return nil
}
