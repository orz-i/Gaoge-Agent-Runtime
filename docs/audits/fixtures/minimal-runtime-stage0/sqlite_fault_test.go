package main

import (
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openAt(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, s := range []string{"PRAGMA journal_mode=WAL", "PRAGMA busy_timeout=3000", "PRAGMA synchronous=FULL"} {
		if _, err = db.Exec(s); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", s, err)
		}
	}
	return db, nil
}
func newDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ready.sqlite")
	db, err := openAt(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, p
}
func sqlMust(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}
func schema(t *testing.T, db *sql.DB) {
	t.Helper()
	sqlMust(t, db, `CREATE TABLE runs(id TEXT PRIMARY KEY,rev INTEGER NOT NULL,epoch INTEGER NOT NULL, owner TEXT, lease INTEGER, state TEXT)`)
	sqlMust(t, db, `CREATE TABLE ready(id TEXT PRIMARY KEY,run_id TEXT NOT NULL REFERENCES runs(id),delivery TEXT NOT NULL)`)
}
func TestAtomicCommitRollback(t *testing.T) {
	db, p := newDB(t)
	schema(t, db)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`INSERT INTO runs VALUES ('r',1,0,NULL,NULL,'{}')`, `INSERT INTO ready VALUES ('r:1','r','ready')`} {
		if _, err = tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	other, err := openAt(p)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var count int
	if err = other.QueryRow(`SELECT count(*) FROM ready WHERE id='r:1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("new connection visibility count=%d err=%v", count, err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE runs SET rev=2 WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO ready VALUES ('r:1','r','ready')`); err == nil {
		t.Fatal("expected duplicate ReadyWork PK")
	}
	_ = tx.Rollback()
	var rev int
	if err = other.QueryRow(`SELECT rev FROM runs WHERE id='r'`).Scan(&rev); err != nil || rev != 1 {
		t.Fatalf("partial commit leaked, rev=%d err=%v", rev, err)
	}
	t.Log("atomic committed ready work visible; failed mutation+duplicate insert rolled back")
}
func TestStaleEpochWithoutRevisionChange(t *testing.T) {
	db, _ := newDB(t)
	schema(t, db)
	now := time.Now().UnixMilli()
	sqlMust(t, db, `INSERT INTO runs VALUES('r',1,1,'A',?, '{}')`, now+30000)
	sqlMust(t, db, `UPDATE runs SET epoch=2,owner='B' WHERE id='r'`)
	r, err := db.Exec(`UPDATE runs SET rev=2 WHERE id='r' AND rev=1 AND epoch=1 AND owner='A' AND lease>?`, now)
	if err != nil {
		t.Fatal(err)
	}
	affected, _ := r.RowsAffected()
	if affected != 0 {
		t.Fatal("stale claimant committed")
	}
	r, err = db.Exec(`UPDATE runs SET rev=2 WHERE id='r' AND rev=1 AND epoch=2 AND owner='B' AND lease>?`, now)
	if err != nil {
		t.Fatal(err)
	}
	affected, _ = r.RowsAffected()
	if affected != 1 {
		t.Fatal("valid claimant rejected")
	}
	t.Log("stale claimant fenced even when run revision had not changed")
}
func TestBusySnapshotRequiresTransactionRestart(t *testing.T) {
	a, p := newDB(t)
	schema(t, a)
	sqlMust(t, a, `INSERT INTO runs VALUES('r',1,0,NULL,NULL,'{}')`)
	b, err := openAt(p)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := a.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var rev int
	if err = tx.QueryRow(`SELECT rev FROM runs WHERE id='r'`).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Exec(`UPDATE runs SET rev=rev+1 WHERE id='r'`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`UPDATE runs SET rev=rev+1 WHERE id='r'`)
	if err == nil {
		_ = tx.Rollback()
		t.Fatal("stale WAL snapshot incorrectly upgraded")
	}
	_ = tx.Rollback()
	snapshotErr := err
	if !strings.Contains(strings.ToLower(snapshotErr.Error()), "busy") && !strings.Contains(strings.ToLower(snapshotErr.Error()), "lock") {
		t.Logf("driver error uses different SQLITE_BUSY snapshot wording: %v", err)
	}
	if _, err = a.Exec(`UPDATE runs SET rev=rev+1 WHERE id='r'`); err != nil {
		t.Fatalf("new txn after stale rollback cannot write: %v", err)
	}
	t.Logf("WAL stale read-write promotion fails and new transaction succeeds: %v", snapshotErr)
}
func TestChildProbe(t *testing.T) {
	mode := os.Getenv("GAOGE_PROBE_MODE")
	if mode == "" {
		return
	}
	p := os.Getenv("GAOGE_PROBE_DB")
	db, err := openAt(p)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(84)
	}
	switch mode {
	case "claim":
		tx, er := db.Begin()
		if er != nil {
			os.Exit(85)
		}
		r, er := tx.Exec(`UPDATE ready SET delivery='claimed' WHERE id='r:1' AND delivery='ready'`)
		if er != nil {
			_ = tx.Rollback()
			os.Exit(86)
		}
		n, _ := r.RowsAffected()
		if er = tx.Commit(); er != nil {
			os.Exit(87)
		}
		fmt.Printf("claimed=%d\n", n)
		os.Exit(0)
	case "crash_before_commit":
		tx, er := db.Begin()
		if er != nil {
			os.Exit(88)
		}
		if _, er = tx.Exec(`UPDATE runs SET rev=99 WHERE id='r'`); er != nil {
			os.Exit(89)
		}
		os.Exit(73)
	case "crash_after_commit":
		tx, er := db.Begin()
		if er != nil {
			os.Exit(90)
		}
		if _, er = tx.Exec(`UPDATE runs SET rev=3 WHERE id='r'`); er != nil {
			os.Exit(91)
		}
		if er = tx.Commit(); er != nil {
			os.Exit(92)
		}
		os.Exit(74)
	}
	os.Exit(93)
}
func child(t *testing.T, p, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestChildProbe$")
	cmd.Env = append(os.Environ(), "GAOGE_PROBE_MODE="+mode, "GAOGE_PROBE_DB="+p)
	return cmd
}
func TestCrossProcessClaim(t *testing.T) {
	db, p := newDB(t)
	schema(t, db)
	sqlMust(t, db, `INSERT INTO runs VALUES('r',1,0,NULL,NULL,'{}')`)
	sqlMust(t, db, `INSERT INTO ready VALUES('r:1','r','ready')`)
	cmdA := child(t, p, "claim")
	cmdB := child(t, p, "claim")
	if err := cmdA.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmdB.Start(); err != nil {
		t.Fatal(err)
	}
	errA := cmdA.Wait()
	errB := cmdB.Wait()
	if errA != nil || errB != nil {
		t.Fatalf("claim subprocess A=%v B=%v", errA, errB)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM ready WHERE delivery='claimed'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("claim count=%d err=%v", count, err)
	}
	t.Log("two competing subprocess claims: one row claimed")
}
func TestProcessExitTransactionBoundary(t *testing.T) {
	db, p := newDB(t)
	schema(t, db)
	sqlMust(t, db, `INSERT INTO runs VALUES('r',1,0,NULL,NULL,'{}')`)
	for _, step := range []struct {
		mode string
		code int
		want int
	}{{"crash_before_commit", 73, 1}, {"crash_after_commit", 74, 3}} {
		cmd := child(t, p, step.mode)
		err := cmd.Run()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != step.code {
			t.Fatalf("%s exit=%v expected=%d", step.mode, err, step.code)
		}
		var rev int
		if err = db.QueryRow(`SELECT rev FROM runs WHERE id='r'`).Scan(&rev); err != nil || rev != step.want {
			t.Fatalf("%s rev=%d want=%d err=%v", step.mode, rev, step.want, err)
		}
	}
	t.Log("child process exit before COMMIT rolls back, exit after COMMIT persists")
}
