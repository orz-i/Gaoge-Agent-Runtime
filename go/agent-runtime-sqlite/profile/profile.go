// Package profile opens private SQLite databases with durable, no-overwrite
// publication, platform access checks and immediate write transactions.
package profile

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	_ "modernc.org/sqlite"
)

// Setup initializes an unpublished database or validates an existing schema.
// The pool is owned by the caller only after CreateNew or OpenExisting succeeds.
type Setup func(context.Context, *sql.DB) error

var (
	ErrUnsupportedSchema = errors.New("unsupported SQLite runtime schema")
	ErrInsecurePath      = errors.New("SQLite path permissions or file type are insecure")
)

// CreateNew initializes an unpublished temporary database in the private
// Profile directory and publishes its completed main DB file atomically via
// a no-overwrite hard link. An interrupted initial initialization therefore
// never leaves a version-0 database at the requested path. Hard-link support
// is required for this opt-in desktop prototype; fail closed otherwise.
func CreateNew(ctx context.Context, path string, initialize, validate Setup) (*sql.DB, error) {
	if initialize == nil || validate == nil || ctx == nil || strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, kernel.ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateDirectory(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	if err = checkNewDestination(abs); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".init-*")
	if err != nil {
		return nil, err
	}
	staged := tmp.Name()
	// Only files created under this unique name in this invocation may be
	// removed on error. Never remove the requested destination or its WAL.
	defer func() {
		_ = os.Remove(staged)
		_ = os.Remove(staged + "-wal")
		_ = os.Remove(staged + "-shm")
	}()
	if err = tmp.Close(); err != nil {
		return nil, err
	}
	stagedStore, err := OpenExisting(ctx, staged, initialize)
	if err != nil {
		return nil, err
	}
	// Check the completed schema on the unpublished database. A mismatched
	// initializer or an invalid authority must never occupy the final path.
	if err = validate(ctx, stagedStore); err != nil {
		_ = stagedStore.Close()
		return nil, err
	}
	if err = stagedStore.Close(); err != nil {
		return nil, err
	}
	// Do not publish a DB requiring an uncommitted sidecar. The final close
	// checkpoints WAL into the main file; unexpected sidecars fail closed.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Lstat(staged + suffix); statErr == nil {
			return nil, fmt.Errorf("%w: unpublished SQLite sidecar remains", ErrUnsupportedSchema)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, statErr
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = checkNewDestination(abs); err != nil {
		return nil, err
	}
	// Unlike Rename, Link never replaces an existing DB and publishes only
	// the fully initialized, checkpointed main file on the same filesystem.
	if err = os.Link(staged, abs); err != nil {
		return nil, err
	}
	// The link is already a complete database. If syncing the parent fails,
	// report uncertain publication durability; callers must inspect/reopen
	// the target, NEVER delete/replace it or silently call CreateNew again.
	dir, err := openProfileDirectoryForSync(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("published SQLite directory cannot be synced: %w", err)
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil {
		return nil, fmt.Errorf("published SQLite directory sync failed: %w", syncErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("published SQLite directory close failed: %w", closeErr)
	}
	return OpenExisting(ctx, abs, validate)
}

func checkNewDestination(abs string) error {
	if _, statErr := os.Lstat(abs); statErr == nil {
		return os.ErrExist
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, statErr := os.Lstat(abs + suffix); statErr == nil {
			return fmt.Errorf("%w: orphan SQLite sidecar exists: %s", ErrInsecurePath, abs+suffix)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
	}
	return nil
}

// OpenExisting checks the private file and its sidecars, then runs validate on
// the same connection pool returned to the caller. It never creates a file.
func OpenExisting(ctx context.Context, path string, validate Setup) (*sql.DB, error) {
	if validate == nil || ctx == nil || strings.TrimSpace(path) == "" || path == ":memory:" {
		return nil, kernel.ErrInvalidInput
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = validatePrivateDirectory(filepath.Dir(abs)); err != nil {
		return nil, err
	}
	// Check existing sidecars before SQLite reads them.
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = validateOptionalPrivateFile(abs + suffix); err != nil {
			return nil, err
		}
	}
	if err = validatePrivateFile(abs); err != nil {
		return nil, err
	}
	// mode=rw prevents SQLite's normal implicit file creation on reopen.
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		// Windows drive letters belong to the URI path, not its authority.
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	params := url.Values{}
	params.Set("mode", "rw")
	// Harness orders DATETIME columns by instant, then ID. A canonical UTC
	// encoding also reads back times supplied with arbitrary host zone names.
	params.Set("_timezone", "UTC")
	params.Set("_time_format", "sqlite")
	params.Add("_pragma", "foreign_keys(ON)")
	// A native busy handler may outlive cancellation; bound lock waiting.
	params.Add("_pragma", "busy_timeout(500)")
	params.Add("_pragma", "synchronous(FULL)")
	params.Set("_txlock", "immediate")
	u.RawQuery = params.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = validate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err = validateOptionalPrivateFile(abs + suffix); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return db, nil
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("SQLite Profile directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: Profile directory is not a real directory", ErrInsecurePath)
	}
	return validateProfilePermissions(path, info)
}
func validatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: SQLite file is not a regular file: %s", ErrInsecurePath, path)
	}
	return validateProfilePermissions(path, info)
}
func validateOptionalPrivateFile(path string) error {
	if err := validatePrivateFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
