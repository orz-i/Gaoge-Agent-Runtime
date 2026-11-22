//go:build !windows

package sqlite

import "os"

func openProfileDirectoryForSync(path string) (*os.File, error) {
	return os.Open(path)
}
