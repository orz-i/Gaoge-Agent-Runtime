package sqlite

import (
	"os"

	"golang.org/x/sys/windows"
)

// FlushFileBuffers requires write access; os.Open returns a read-only handle.
func openProfileDirectoryForSync(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open for directory sync", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}
