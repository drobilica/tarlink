//go:build linux

package app

import "os"

func effectiveUID() int { return os.Geteuid() }
