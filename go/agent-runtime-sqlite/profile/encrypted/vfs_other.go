//go:build !windows

package encrypted

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/ncruces/go-sqlite3/util/vfsutil"
	"github.com/ncruces/go-sqlite3/vfs"
	"github.com/ncruces/go-sqlite3/vfs/adiantum"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
)

const privateVFSName = "gaoge-private-adiantum"

func init() {
	vfs.Register(privateVFSName, adiantum.Wrap(privateFiles{vfs.Find("")}, nil))
}

type privateFiles struct{ vfs.VFS }

// The OS VFS requests 0666 for named sidecars (including the mmap WAL
// index). Reserve those filenames with private permissions before SQLite
// first writes to them, without changing the embedding process's umask.
func (fs privateFiles) OpenFilename(name *vfs.Filename, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	if name != nil && flags&vfs.OPEN_CREATE != 0 && flags&(vfs.OPEN_WAL|vfs.OPEN_MAIN_JOURNAL) != 0 {
		path := name.String()
		created, err := reserveSidecar(path, flags&vfs.OPEN_EXCLUSIVE != 0)
		if err != nil {
			return nil, flags, err
		}
		if flags&vfs.OPEN_WAL != 0 {
			shm, err := reserveSidecar(name.Database()+"-shm", false)
			if err != nil {
				return nil, flags, err
			}
			created = created || shm
		}
		if created {
			dir, err := os.Open(filepath.Dir(path))
			if err != nil {
				return nil, flags, err
			}
			if err = errors.Join(dir.Sync(), dir.Close()); err != nil {
				return nil, flags, err
			}
		}
		// Our exclusive create already applied the requested semantics.
		// A path removed concurrently must not be recreated with 0666.
		flags &^= vfs.OPEN_CREATE | vfs.OPEN_EXCLUSIVE
	}
	return vfsutil.WrapOpenFilename(fs.VFS, name, flags)
}

func reserveSidecar(path string, exclusive bool) (bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		return true, f.Close()
	}
	if exclusive || !errors.Is(err, os.ErrExist) {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return false, profile.ErrInsecurePath
	}
	return false, nil
}
