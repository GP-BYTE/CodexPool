//go:build windows

package securefs

import "errors"

var (
	ErrBusy        = errors.New("resource busy")
	ErrUnsupported = errors.New("secure Windows file locking is not implemented in this preview")
)

type Lock struct{}

func TryLock(path string) (*Lock, error) {
	return nil, ErrUnsupported
}

func (l *Lock) Close() error { return nil }
