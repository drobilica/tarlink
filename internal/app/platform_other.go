//go:build !linux

package app

func effectiveUID() int { return -1 }
