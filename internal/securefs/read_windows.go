//go:build windows

package securefs

func ReadPrivateFile(path string, maxBytes int64) ([]byte, error) {
	return nil, errWindowsACLUnverified
}
