package sqlite_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"golang.org/x/sys/windows"
)

func TestWindowsUnconfirmedPublicationReopensOriginal(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "retained.sqlite")
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	// Deny writing extended attributes on the directory itself. Creating the
	// DB and its hard link can succeed, but opening GENERIC_WRITE to flush cannot.
	if err := testprofile.SetDACL(dir, "D:P(D;;0x10;;;"+sid+")(A;OICI;FA;;;"+sid+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.CreateNew(t.Context(), path); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("unconfirmed publication = %v", err)
	}
	original, err := os.Stat(path)
	if err != nil {
		t.Fatalf("published file missing: %v", err)
	}
	if _, err := sqlite.CreateNew(t.Context(), path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("second enrollment = %v", err)
	}
	store, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := os.Stat(path)
	if err != nil || !os.SameFile(original, reopened) {
		t.Fatalf("published file identity changed: %v", err)
	}
}

func TestWindowsProfileRequiresPrivateFileInheritance(t *testing.T) {
	user, err := windows.GetCurrentThreadEffectiveToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	owner := "(A;;FA;;;" + user.User.Sid.String() + ")"
	for _, tt := range []struct{ name, dacl string }{
		{"creator defaults", "D:P" + owner},
		{"public files", "D:P" + owner + "(A;OIIO;GR;;;WD)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := privateDir(t)
			if err := testprofile.SetDACL(dir, tt.dacl); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "new.sqlite")
			if _, err := sqlite.CreateNew(t.Context(), path); !errors.Is(err, sqlite.ErrInsecurePath) {
				t.Fatalf("unsafe child inheritance accepted: %v", err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected published file: %v", err)
			}
		})
	}
}

func TestWindowsReopenRejectsPublicSidecars(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			path := filepath.Join(privateDir(t), "existing.sqlite")
			store, err := sqlite.CreateNew(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path+suffix, []byte("retained sidecar"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = testprofile.Chmod(path+suffix, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err = sqlite.OpenExisting(t.Context(), path); !errors.Is(err, sqlite.ErrInsecurePath) {
				t.Fatalf("public sidecar accepted: %v", err)
			}
			if data, err := os.ReadFile(path + suffix); err != nil || string(data) != "retained sidecar" {
				t.Fatalf("original sidecar changed: %q, %v", data, err)
			}
		})
	}
}

func TestWindowsLiveDatabaseAndSidecarsInheritPrivateACL(t *testing.T) {
	dir := privateDir(t)
	reference := filepath.Join(dir, "private-file")
	if err := os.WriteFile(reference, nil, 0600); err != nil {
		t.Fatal(err)
	}
	dacl := func(path string) string {
		t.Helper()
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return sd.String()
	}
	want := dacl(reference)
	if want == "" {
		t.Fatal("missing reference DACL")
	}
	path := filepath.Join(dir, "live.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Create(t.Context(), sampleCreate("private")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		if dacl(name) != want {
			t.Errorf("%s did not inherit the private file DACL", filepath.Base(name))
		}
	}
}
