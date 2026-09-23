package server

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isCrossDevice reports whether a rename failed because source and target
// live on different volumes. Windows reports ERROR_NOT_SAME_DEVICE; Go's
// syscall.EXDEV there is an invented value no Windows API returns.
func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}
