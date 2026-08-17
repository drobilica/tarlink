package cli

import (
	"errors"

	"github.com/drobilica/tarlink/internal/app"
)

const (
	exitInvalidArguments    = 2
	exitUnsupportedPlatform = 3
	exitRegistry            = 4
	exitNetwork             = 5
	exitChecksum            = 6
	exitArchive             = 7
	exitNotFound            = 8
	exitAlreadyInstalled    = 9
	exitNotInstalled        = 10
	exitNoUpdate            = 11
	exitLockConflict        = 12
	exitStateCorrupt        = 13
	exitPermission          = 14
	exitConflict            = 15
	exitRoot                = 16
	exitOther               = 1
)

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	switch app.CodeOf(err) {
	case app.CodeInvalidArguments:
		return exitInvalidArguments
	case app.CodeUnsupportedPlatform:
		return exitUnsupportedPlatform
	case app.CodeRegistry:
		return exitRegistry
	case app.CodeNetwork:
		return exitNetwork
	case app.CodeChecksum:
		return exitChecksum
	case app.CodeArchive:
		return exitArchive
	case app.CodeNotFound:
		return exitNotFound
	case app.CodeAlreadyInstalled:
		return exitAlreadyInstalled
	case app.CodeNotInstalled:
		return exitNotInstalled
	case app.CodeNoUpdate:
		return exitNoUpdate
	case app.CodeLockConflict:
		return exitLockConflict
	case app.CodeStateCorrupt:
		return exitStateCorrupt
	case app.CodePermission:
		return exitPermission
	case app.CodeConflict:
		return exitConflict
	case app.CodeRoot:
		return exitRoot
	default:
		var pathError interface{ Unwrap() error }
		if errors.As(err, &pathError) {
			return exitOther
		}
		return exitOther
	}
}
