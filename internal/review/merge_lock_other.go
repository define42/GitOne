//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package review

import (
	"errors"
	"os"
)

func lockMergeFile(_ *os.File) (func() error, error) {
	return nil, errors.New("cross-process merge locking is not supported on this platform")
}
