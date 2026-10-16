package sqlite_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPrivateCreateAndFailClosedReopen(t *testing.T) {
	ctx := t.Context()
	dir := privateDir(t)
	path := filepath.Join(dir, "private.sqlite")
	if _, err := sqlite.Open(ctx, path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing existing DB unexpectedly opened: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Open created DB: %v", err)
	}
	s, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("created DB must be private: %04o", info.Mode().Perm())
	}
	if _, err := sqlite.CreateNew(ctx, path); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive CreateNew did not refuse existing DB: %v", err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Create(ctx, sampleCreate("retained")); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if side, err := os.Stat(path + sidecar); err == nil && side.Mode().Perm()&0077 != 0 {
			t.Fatalf("sidecar %s exposed: %04o", sidecar, side.Mode().Perm())
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	moved := path + ".displaced"
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.OpenExisting(ctx, path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing restore silently initialized new DB: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing database re-created: %v", err)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatal(err)
	}
	restored, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := restored.Load(ctx, "retained")
	if err != nil || snap.Run.ID != "retained" {
		t.Fatalf("lost original Run after file restored: snapshot=%#v err=%v", snap, err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivatePathAndSymlinkRequirements(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "runtime.sqlite")
	s, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(t.Context(), path); !errors.Is(err, sqlite.ErrInsecurePath) {
		t.Fatalf("insecure SQLite file accepted: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(t.Context(), path); !errors.Is(err, sqlite.ErrInsecurePath) {
		t.Fatalf("insecure Profile directory accepted: %v", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias.sqlite")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Open(t.Context(), link); !errors.Is(err, sqlite.ErrInsecurePath) {
		t.Fatalf("symlink accepted: %v", err)
	}
	if _, err := sqlite.CreateNew(t.Context(), link); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive create replaced link: %v", err)
	}
	publicDir := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(publicDir, 0755); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(publicDir, "private.sqlite")
	if _, err := sqlite.CreateNew(t.Context(), public); !errors.Is(err, sqlite.ErrInsecurePath) {
		t.Fatalf("created DB in publicly accessible dir: %v", err)
	}
	if _, err := os.Stat(public); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe database was created: %v", err)
	}
}

func TestStage1SchemaStructuralValidation(t *testing.T) {
	path := filepath.Join(privateDir(t), "schema.sqlite")
	s, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`DROP INDEX runtime_ready_due`); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.Open(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("missing due index accepted: %v", err)
	}
	if _, err = db.Exec(`CREATE INDEX runtime_ready_due ON runtime_ready_work(quarantine_code, available_ns, committed_ns, work_id)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE runtime_runs ADD COLUMN unexpected TEXT`); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.Open(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("unexpected schema alteration accepted: %v", err)
	}
}

func TestUnsupportedExperimentalSchemaVersionMustNotBeMigrated(t *testing.T) {
	path := filepath.Join(privateDir(t), "legacy.sqlite")
	s, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rt.Create(t.Context(), sampleCreate("preserve")); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.Open(t.Context(), path); !errors.Is(err, sqlite.ErrUnsupportedSchema) {
		t.Fatalf("unknown experimental schema version implicitly migrated: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM runtime_runs WHERE run_id='preserve'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("version rejection destroyed data count=%d err=%v", count, err)
	}
}

func TestQuarantineCorruptReadyWithoutBlockingHealthyWork(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "poison.sqlite")
	s, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		req := sampleCreate(id)
		req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
		if _, err = rt.Create(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec(`UPDATE runtime_ready_work SET events_json=x'7b' WHERE run_id='a'`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Minute)
	claims, err := s.ClaimTransitions(ctx, kernel.TransitionClaimRequest{WorkerID: "pump", Limit: 8, LeaseDuration: time.Minute, Now: now})
	if err != nil || len(claims) != 1 || claims[0].Transition.RunID != "b" {
		t.Fatalf("healthy run blocked by corrupt work: claims=%#v err=%v", claims, err)
	}
	quarantine, err := s.ListQuarantinedTransitions(ctx, 10)
	if err != nil || len(quarantine) != 1 || quarantine[0].WorkID != "a:1" || quarantine[0].FailureCode != "invalid_events" {
		t.Fatalf("quarantine diagnostics=%#v err=%v", quarantine, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM runtime_ready_work WHERE run_id='a' AND quarantine_code='invalid_events'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost poisoned work: count=%d err=%v", count, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	remembered, err := reopened.ListQuarantinedTransitions(ctx, 10)
	if err != nil || len(remembered) != 1 {
		t.Fatalf("quarantine lost on restart=%#v err=%v", remembered, err)
	}
	snap, err := reopened.Load(ctx, "a")
	if err != nil || snap.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("Run mutated by quarantine: %#v %v", snap, err)
	}
}

func TestFarFutureRetryAndLeaseDoNotOverflow(t *testing.T) {
	ctx := t.Context()
	s := openStore(t)
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("future")
	req.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true}}
	if _, err = rt.Create(ctx, req); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claims, err := s.ClaimTransitions(ctx, kernel.TransitionClaimRequest{WorkerID: "first", Limit: 1, Now: now, LeaseDuration: time.Minute})
	if err != nil || len(claims) != 1 {
		t.Fatalf("initial claim: %#v %v", claims, err)
	}
	far := time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)
	lease := kernel.TransitionLeaseRequest{TransitionID: claims[0].Transition.ID, LeaseID: claims[0].LeaseID, WorkerID: claims[0].WorkerID}
	err = s.RetryTransition(ctx, kernel.TransitionRetryRequest{TransitionLeaseRequest: lease, AvailableAt: far})
	if !errors.Is(err, sqlite.ErrInvalidTime) {
		t.Fatalf("overflowed far-future RetryAt accepted: %v", err)
	}
	if _, err = s.ClaimTransitions(ctx, kernel.TransitionClaimRequest{WorkerID: "second", Limit: 1, Now: far, LeaseDuration: time.Minute}); !errors.Is(err, sqlite.ErrInvalidTime) {
		t.Fatalf("out-of-range claim clock accepted: %v", err)
	}
	next, err := s.ClaimTransitions(ctx, kernel.TransitionClaimRequest{WorkerID: "second", Limit: 1, Now: now.Add(time.Minute), LeaseDuration: time.Minute})
	if err != nil || len(next) != 1 {
		t.Fatalf("valid Retry must stay available: %v err=%v", next, err)
	}
	bad := sampleCreate("invalid-wakeup")
	bad.Events = []kernel.EventDraft{{Type: "agent.started", Wakeup: true, WakeupAt: &far}}
	if _, err = rt.Create(ctx, bad); !errors.Is(err, sqlite.ErrInvalidTime) {
		t.Fatalf("far WakeupAt not rejected: %v", err)
	}
	if _, err = rt.Load(ctx, bad.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("invalid timestamp still created Run: %v", err)
	}
}

func TestTerminalProgressPreservesUnconsumedOutboxAndProofs(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(privateDir(t), "terminal.sqlite")
	s, err := sqlite.CreateNew(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	req := sampleCreate("terminal")
	req.Events = []kernel.EventDraft{{Type: "started", Wakeup: true}}
	current, err := rt.Create(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		current, err = rt.Apply(ctx, req.ID, current.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"phase":"running"}`), Events: []kernel.EventDraft{{Type: "next", Wakeup: true}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var before int
	if err = db.QueryRow("SELECT count(*) FROM runtime_ready_work WHERE run_id='terminal'").Scan(&before); err != nil || before != 4 {
		t.Fatalf("lost pending recovery before terminal: %d %v", before, err)
	}
	done, err := rt.Apply(ctx, req.ID, current.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"phase":"finished"}`), Result: &kernel.Result{ContentType: "text/plain", Content: json.RawMessage(`"done"`)}, Events: []kernel.EventDraft{{Type: "terminal"}}})
	if err != nil {
		t.Fatal(err)
	}
	var remaining, terminalReady, events, proofs int
	for _, tc := range []struct {
		query string
		dst   *int
	}{
		{"SELECT count(*) FROM runtime_ready_work WHERE run_id='terminal'", &remaining},
		{"SELECT count(*) FROM runtime_ready_work WHERE run_id='terminal' AND status='completed'", &terminalReady},
		{"SELECT count(*) FROM runtime_events WHERE run_id='terminal'", &events},
		{"SELECT count(*) FROM runtime_commit_proofs WHERE run_id='terminal'", &proofs},
	} {
		if err = db.QueryRow(tc.query).Scan(tc.dst); err != nil {
			t.Fatal(err)
		}
	}
	if remaining != 5 || terminalReady != 1 || events != 6 || proofs != 5 {
		t.Fatalf("terminal transition lost unconsumed projection or facts: ready=%d terminal=%d events=%d proofs=%d", remaining, terminalReady, events, proofs)
	}
	claims, err := s.ClaimTransitions(ctx, kernel.TransitionClaimRequest{WorkerID: "projector", Limit: 10, Now: time.Now().Add(time.Minute), LeaseDuration: time.Minute})
	if err != nil || len(claims) != 5 || claims[0].Transition.Revision != 1 || claims[4].Transition.Revision != done.Run.Revision {
		t.Fatalf("unconsumed outbox must match Kernel Memory Store: %#v err=%v", claims, err)
	}
	// Every projection can be acknowledged independently after its consumer
	// has proved consumption, even after the Run becomes terminal.
	for _, claim := range claims {
		if err := s.AckTransition(ctx, kernel.TransitionLeaseRequest{
			TransitionID: claim.Transition.ID, LeaseID: claim.LeaseID, WorkerID: claim.WorkerID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRow("SELECT count(*) FROM runtime_ready_work WHERE run_id='terminal'").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("acknowledged transitions still pending count=%d err=%v", remaining, err)
	}
}

// A batch size of one must make eventual progress even if its first eligible
// row is unreadable; quarantine is durable and the next poll can claim healthy.
func TestPoisonAtPageHeadDoesNotStarveLimitOne(t *testing.T) {
	path := filepath.Join(privateDir(t), "head.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b"} {
		req := sampleCreate(id)
		req.Events = []kernel.EventDraft{{Type: "started", Wakeup: true}}
		if _, err = rt.Create(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err = db.Exec("UPDATE runtime_ready_work SET events_json=x'7b' WHERE run_id='a'"); err != nil {
		t.Fatal(err)
	}
	claim := kernel.TransitionClaimRequest{WorkerID: "single", Now: time.Now().Add(time.Minute), Limit: 1, LeaseDuration: time.Minute}
	first, err := store.ClaimTransitions(t.Context(), claim)
	if err != nil || len(first) != 0 {
		t.Fatalf("expected quarantine first, got=%#v err=%v", first, err)
	}
	second, err := store.ClaimTransitions(t.Context(), claim)
	if err != nil || len(second) != 1 || second[0].Transition.RunID != "b" {
		t.Fatalf("healthy row stuck behind poison=%#v err=%v", second, err)
	}
}

func TestLeaseExpirationBeyondNanosecondMaximumIsRejected(t *testing.T) {
	store := openStore(t)
	nearLimit := time.Date(2262, time.April, 11, 0, 0, 0, 0, time.UTC)
	claim := kernel.TransitionClaimRequest{WorkerID: "late", Limit: 1, Now: nearLimit, LeaseDuration: 48 * time.Hour}
	if _, err := store.ClaimTransitions(t.Context(), claim); !errors.Is(err, sqlite.ErrInvalidTime) {
		t.Fatalf("overflowed lease expiry: %v", err)
	}
}

func TestCreateNewRejectsOrphanWALWithoutTouchingDatabase(t *testing.T) {
	path := filepath.Join(privateDir(t), "replaced.sqlite")
	if err := os.WriteFile(path+"-wal", []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.CreateNew(t.Context(), path); !errors.Is(err, sqlite.ErrInsecurePath) {
		t.Fatalf("orphan WAL ignored: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan protection left a new DB: %v", err)
	}
	data, err := os.ReadFile(path + "-wal")
	if err != nil || string(data) != "orphan" {
		t.Fatalf("orphan sidecar was rewritten: %q err=%v", data, err)
	}
}
