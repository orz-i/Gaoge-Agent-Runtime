//go:build !windows

package sqlite

import (
	"fmt"
	"os"
)

func openProfileDirectoryForSync(path string) (*os.File, error) {
	return os.Open(path)
}

func validateProfilePermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 == 0 {
		return nil
	}
	if info.IsDir() {
		return fmt.Errorf("%w: Profile directory must be private (0700): %s", ErrInsecurePath, path)
	}
	return fmt.Errorf("%w: SQLite file must be private (0600): %s", ErrInsecurePath, path)
}
