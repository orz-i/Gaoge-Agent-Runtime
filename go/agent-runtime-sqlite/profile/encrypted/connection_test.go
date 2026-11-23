package encrypted_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/vfs"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
)

func TestEncryptedStagingReopenAndDiskTemp(t *testing.T) {
	ctx := t.Context()
	dir := testprofile.Dir(t)
	path := filepath.Join(dir, "store.sqlite")
	var key [32]byte
	rand.Read(key[:])
	defer clear(key[:])
	open := encrypted.WithKey(&key)
	marker := "private-" + rand.Text()
	when := time.Date(2026, 10, 11, 13, 14, 15, 123456789, time.FixedZone("source", 9*3600))
	check := func(ctx context.Context, db *sql.DB) error {
		var text string
		var timestamp time.Time
		if err := db.QueryRowContext(ctx, "SELECT data, at FROM records").Scan(&text, &timestamp); err != nil {
			return err
		}
		if text != marker || !timestamp.Equal(when) || timestamp.Location() != time.UTC {
			return errors.New("data or timestamp changed")
		}
		return nil
	}
	init := func(ctx context.Context, db *sql.DB) error {
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL; CREATE TABLE records(data TEXT, at DATETIME)"); err != nil {
			return err
		}
		stmt, err := db.PrepareContext(ctx, "INSERT INTO records VALUES (?,?)")
		if err != nil {
			return err
		}
		_, err = stmt.ExecContext(ctx, marker, when)
		if err = errors.Join(err, stmt.Close()); err != nil {
			return err
		}
		if _, err = db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
			return err
		}
		matches, err := filepath.Glob(filepath.Join(dir, ".store.sqlite.init-*"))
		if err != nil {
			return err
		}
		for _, f := range matches {
			if strings.HasSuffix(f, "-wal") || strings.HasSuffix(f, "-shm") {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if len(data) < 4096 || bytes.Contains(data, []byte(marker)) || bytes.Contains(data, []byte("SQLite format 3")) {
				return errors.New("staging file exposed database contents")
			}
			return nil
		}
		return errors.New("missing unpublished database")
	}
	db, err := open.CreateNew(ctx, path, init, check)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var wrong [32]byte
	for _, other := range []profile.OpenConnection{nil, encrypted.WithKey(&wrong), encrypted.WithKey(nil)} {
		if pool, err := other.OpenExisting(ctx, path, check); err == nil {
			pool.Close()
			t.Fatal("opened without the enrolled key")
		}
	}
	db, err = open.OpenExisting(ctx, path, check)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxIdleConns(0)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "PRAGMA temp_store=FILE; PRAGMA temp.cache_size=5; CREATE TEMP TABLE spill(v BLOB)"); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte(marker), 90)
	if _, err = conn.ExecContext(ctx, "WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<512) INSERT INTO spill SELECT ? FROM n", payload); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err = conn.QueryRowContext(ctx, "SELECT count(*) FROM spill WHERE v=?", payload).Scan(&rows); err != nil || rows != 512 {
		t.Fatalf("temp rows=%d: %v", rows, err)
	}
	err = conn.Raw(func(c any) error {
		file, err := c.(sqlitedriver.Conn).Raw().FileControl("temp", sqlite3.FCNTL_FILE_POINTER)
		if err != nil {
			return err
		}
		wrapped, ok := file.(interface {
			vfs.File
			Unwrap() vfs.File
		})
		if !ok {
			return errors.New("temp database has no encrypted file backing")
		}
		disk := wrapped.Unwrap()
		for {
			layer, ok := disk.(interface{ Unwrap() vfs.File })
			if !ok {
				break
			}
			disk = layer.Unwrap()
		}
		size, err := disk.Size()
		if err != nil {
			return err
		}
		if size < 1<<20 || size > 8<<20 {
			return errors.New("temp database did not spill to disk")
		}
		// Use the already-open OS file below the VFS. The native temp file is
		// anonymous or delete-on-close. The SQLite side of the same file must
		// contain the marker, to positively identify the bytes under test.
		plain, err := io.ReadAll(io.NewSectionReader(wrapped, 0, size))
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.NewSectionReader(disk, 0, size))
		if err != nil {
			return err
		}
		if int64(len(raw)) != size || !bytes.Contains(plain, payload) || bytes.Contains(raw, []byte(marker)) || bytes.Contains(raw, []byte("SQLite format 3")) || bytes.Equal(raw, make([]byte, len(raw))) {
			return errors.New("invalid temporary ciphertext evidence")
		}
		t.Logf("disk temp backing=%T bytes=%d rows=%d, marker found only above encryption", disk, size, rows)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
