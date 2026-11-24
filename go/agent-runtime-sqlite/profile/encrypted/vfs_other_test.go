//go:build !windows

package encrypted

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
)

func TestReservePrivateSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite-wal")
	if created, err := reserveSidecar(path, true); err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("new sidecar permissions: %v", info.Mode().Perm())
	}
	if created, err := reserveSidecar(path, false); err != nil || created {
		t.Fatalf("reopen: created=%v err=%v", created, err)
	}
	if _, err := reserveSidecar(path, true); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive create on occupied path: %v", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveSidecar(path, false); !errors.Is(err, profile.ErrInsecurePath) {
		t.Fatalf("reopened a non-private sidecar: %v", err)
	}
	link := path + ".link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveSidecar(link, false); !errors.Is(err, profile.ErrInsecurePath) {
		t.Fatalf("accepted a symlink: %v", err)
	}
}
