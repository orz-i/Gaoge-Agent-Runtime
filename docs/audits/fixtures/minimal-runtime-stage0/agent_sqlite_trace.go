package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"log"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type stepModel struct{ steps, calls int }

func (m *stepModel) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	m.calls++
	if m.calls <= m.steps {
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("step-%d", m.calls), ToolKey: "observe", Arguments: json.RawMessage(fmt.Sprintf(`{"index":%d}`, m.calls))}}, Usage: &model.Usage{InputTokens: 8, OutputTokens: 4}}, nil
	}
	return model.Response{Content: "complete", Usage: &model.Usage{InputTokens: 8, OutputTokens: 4}}, nil
}
func main() {
	if len(os.Args) < 3 {
		log.Fatal("usage: bench <steps> <path>")
	}
	n, err := strconv.Atoi(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	out := os.Args[2]
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "observe", Name: "Observe", InputSchema: json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"}},"required":["index"]}`)}, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{"step":%s,"text":%q}`, strings.TrimSuffix(strings.TrimPrefix(string(r.Call.Arguments), `{"index":`), "}"), strings.Repeat("x", 64))), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		log.Fatal("registry", err)
	}
	dbPath := filepath.Join(os.TempDir(), fmt.Sprintf("gaoge-real-%d-%d.sqlite", n, time.Now().UnixNano()))
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA wal_autocheckpoint=0", "CREATE TABLE runs(id TEXT PRIMARY KEY,revision INTEGER,state BLOB)", "CREATE TABLE proofs(id TEXT PRIMARY KEY,revision INTEGER)"} {
		if _, err = db.Exec(q); err != nil {
			log.Fatal(err)
		}
	}
	trace := &traceStore{Store: memory.NewStore(), db: db}
	rt, err := kernel.New(kernel.Dependencies{Store: trace})
	if err != nil {
		log.Fatal(err)
	}
	m := &stepModel{steps: n}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Limits: agent.Limits{MaxLLMCalls: n + 4, MaxToolCalls: n + 4}})
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	snap, err := runner.StartRun(context.Background(), agent.StartRequest{ID: fmt.Sprintf("run-%d", n), Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile-1"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "inspect varying inputs", ToolKeys: []string{"observe"}})
	elapsed := time.Since(start)
	if err != nil {
		log.Fatalf("steps=%d err=%v status=%s calls=%d revision=%d", n, err, snap.Run.Status, m.calls, snap.Run.Revision)
	}
	view, err := agent.ViewState(snap)
	if err != nil {
		log.Fatal(err)
	}
	if snap.Run.Status != kernel.RunStatusCompleted {
		log.Fatalf("steps=%d status=%s calls=%d revision=%d", n, snap.Run.Status, m.calls, snap.Run.Revision)
	}
	if err := os.WriteFile(out, snap.State, 0600); err != nil {
		log.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("steps=%d wall_ms=%d model_calls=%d revision=%d messages=%d invocations=%d state_bytes=%d heap_alloc_bytes=%d total_alloc_mb=%.1f event_head=%d\n", n, elapsed.Milliseconds(), m.calls, snap.Run.Revision, len(view.Messages), len(view.ModelInvocations), len(snap.State), mem.HeapAlloc, float64(mem.TotalAlloc)/1048576, snap.EventHead)
	trace.report(dbPath)
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
}

type traceStore struct {
	kernel.Store
	db              *sql.DB
	durations       []int64
	totalStateBytes int64
}

func (s *traceStore) Create(ctx context.Context, r kernel.Record, e []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, r, e)
	if err != nil {
		return snap, err
	}
	return snap, s.persist(ctx, snap, true)
}
func (s *traceStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	snap, err := s.Store.Apply(ctx, id, rev, m)
	if err != nil {
		return snap, err
	}
	return snap, s.persist(ctx, snap, false)
}
func (s *traceStore) persist(ctx context.Context, snap kernel.Snapshot, initial bool) error {
	start := time.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if initial {
		_, err = tx.ExecContext(ctx, "INSERT INTO runs(id,revision,state) VALUES(?,?,?)", snap.Run.ID, snap.Run.Revision, snap.State)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE runs SET revision=?,state=? WHERE id=?", snap.Run.Revision, snap.State, snap.Run.ID)
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO proofs(id,revision) VALUES(?,?)", fmt.Sprintf("%s:rev:%d", snap.Run.ID, snap.Run.Revision), snap.Run.Revision)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	s.durations = append(s.durations, time.Since(start).Nanoseconds())
	s.totalStateBytes += int64(len(snap.State))
	return nil
}
func (s *traceStore) report(path string) {
	sort.Slice(s.durations, func(i, j int) bool { return s.durations[i] < s.durations[j] })
	idx := func(q float64) int { return int(float64(len(s.durations)-1)*q + 0.5) }
	data, _ := os.Stat(path)
	wal, _ := os.Stat(path + "-wal")
	fmt.Printf("REAL AGENT SNAPSHOT->SQL TRACE: commits=%d sum_state_bytes=%d DB_bytes=%d WAL_bytes=%d p50_ms=%.3f p95_ms=%.3f (not atomic Memory+SQLite, I/O only)\n", len(s.durations), s.totalStateBytes, data.Size(), wal.Size(), float64(s.durations[idx(.5)])/1e6, float64(s.durations[idx(.95)])/1e6)
}
