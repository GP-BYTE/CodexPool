//go:build windows

package securefs

import (
	"errors"
	"os"
)

var errWindowsACLUnverified = errors.New("secure Windows owner/DACL validation is not implemented in this preview")

func validateOwnedInfo(info os.FileInfo, path string) error {
	return errWindowsACLUnverified
}

func validatePrivateRegularInfo(info os.FileInfo, path string) error {
	return errWindowsACLUnverified
}

func hardenPrivateDir(path string) error {
	return errWindowsACLUnverified
}
