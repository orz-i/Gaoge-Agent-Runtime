//go:build !windows

package testprofile

import "os"

func Chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
