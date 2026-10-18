package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

type runRelationChildResult struct {
	Relation    runrelation.Relation
	Disposition string
	BusyRetries int
}

func TestSQLiteRunRelationSubprocessChild(t *testing.T) {
	mode := os.Getenv("GAOGE_SQLITE_REL_PROCESS")
	if mode == "" {
		return
	}
	ctx := context.Background()
	path := os.Getenv("GAOGE_SQLITE_REL_DB")
	relation := sqliteRelation(
		os.Getenv("GAOGE_SQLITE_REL_PARENT"),
		os.Getenv("GAOGE_SQLITE_REL_CHILD"),
		os.Getenv("GAOGE_SQLITE_REL_OWNER"),
		time.Date(2026, 10, 17, 10, 0, 0, 7, time.UTC),
	)
	store, err := sqlite.OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// tx-crash leaves a partially written, uncommitted SQLite transaction
	// and deliberately omits all cleanup and Close.
	if mode == "tx-crash" {
		raw, rawErr := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_txlock=immediate")
		if rawErr != nil {
			t.Fatal(rawErr)
		}
		tx, txErr := raw.BeginTx(ctx, nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		_, txErr = tx.ExecContext(ctx,
			`INSERT INTO runtime_run_relations(child_run_id,parent_run_id,kind,owner_node_id,created_ns) VALUES(?,?,?,?,?)`,
			relation.ChildRunID, relation.ParentRunID, string(relation.Kind), relation.OwnerNodeID, relation.CreatedAt.UnixNano())
		if txErr != nil {
			t.Fatal(txErr)
		}
		os.Exit(84)
	}
	defer func() { _ = store.Close() }()
	if ready := os.Getenv("GAOGE_SQLITE_REL_READY"); ready != "" {
		if err = os.WriteFile(ready, []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		gate := os.Getenv("GAOGE_SQLITE_REL_GATE")
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err = os.Stat(gate); err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
				t.Fatalf("process gate not released: %v", err)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	relations := sqlite.NewRunRelationStore(store)
	var result runRelationChildResult
	for attempt := 0; attempt < 12; attempt++ {
		persisted, reused, putErr := relations.Put(ctx, relation)
		if errors.Is(putErr, sqlite.ErrBusy) {
			result.BusyRetries++
			time.Sleep(10 * time.Millisecond)
			continue
		}
		switch {
		case putErr == nil && reused:
			result.Disposition = "reused"
			result.Relation = persisted
		case putErr == nil:
			result.Disposition = "created"
			result.Relation = persisted
		case errors.Is(putErr, runrelation.ErrConflict):
			result.Disposition = "conflict"
		default:
			t.Fatalf("unexpected child Put error: %v", putErr)
		}
		break
	}
	if result.Disposition == "" {
		t.Fatalf("SQLite remained busy after bounded retries: %+v", result)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("GAOGE_SQLITE_REL_RESULT"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

type relationProcess struct {
	command *exec.Cmd
	stderr  bytes.Buffer
	result  string
	ready   string
}

func launchRelationProcess(t *testing.T, path, mode, parent, child, owner, gate, suffix string) *relationProcess {
	t.Helper()
	process := &relationProcess{
		result: filepath.Join(filepath.Dir(path), "result-"+suffix+".json"),
		ready:  filepath.Join(filepath.Dir(path), "ready-"+suffix),
	}
	process.command = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSQLiteRunRelationSubprocessChild$")
	process.command.Env = append(os.Environ(),
		"GAOGE_SQLITE_REL_PROCESS="+mode,
		"GAOGE_SQLITE_REL_DB="+path,
		"GAOGE_SQLITE_REL_PARENT="+parent,
		"GAOGE_SQLITE_REL_CHILD="+child,
		"GAOGE_SQLITE_REL_OWNER="+owner,
		"GAOGE_SQLITE_REL_RESULT="+process.result,
	)
	if gate != "" {
		process.command.Env = append(process.command.Env,
			"GAOGE_SQLITE_REL_READY="+process.ready,
			"GAOGE_SQLITE_REL_GATE="+gate)
	}
	process.command.Stdout = &process.stderr
	process.command.Stderr = &process.stderr
	if err := process.command.Start(); err != nil {
		t.Fatal(err)
	}
	return process
}

func awaitRelationProcess(t *testing.T, process *relationProcess, expectedExit int) runRelationChildResult {
	t.Helper()
	err := process.command.Wait()
	if expectedExit != 0 {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != expectedExit {
			t.Fatalf("child expected exit=%d, got err=%v: %s", expectedExit, err, process.stderr.String())
		}
		return runRelationChildResult{}
	}
	if err != nil {
		t.Fatalf("child err=%v: %s", err, process.stderr.String())
	}
	data, err := os.ReadFile(process.result)
	if err != nil {
		t.Fatal(err)
	}
	var result runRelationChildResult
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSQLiteRunRelationConcurrentOSProcesses(t *testing.T) {
	for _, tc := range []struct {
		name    string
		parents []string
		childs  []string
		owners  []string
	}{
		{
			name:    "same-owner-different-child",
			parents: []string{"parent", "parent", "parent", "parent"},
			childs:  []string{"child-a", "child-a", "child-b", "child-b"},
			owners:  []string{"owner", "owner", "owner", "owner"},
		},
		{
			name:    "same-child-different-owner",
			parents: []string{"parent-a", "parent-a", "parent-b", "parent-b"},
			childs:  []string{"child", "child", "child", "child"},
			owners:  []string{"owner-a", "owner-a", "owner-b", "owner-b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := privateDir(t)
			path := filepath.Join(base, "relations.sqlite")
			store, err := sqlite.CreateNew(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Close(); err != nil {
				t.Fatal(err)
			}
			gate := filepath.Join(base, "gate")
			processes := make([]*relationProcess, 4)
			for i := range processes {
				processes[i] = launchRelationProcess(t, path, "put",
					tc.parents[i], tc.childs[i], tc.owners[i], gate, string(rune('a'+i)))
			}
			deadline := time.Now().Add(10 * time.Second)
			for _, process := range processes {
				for {
					if _, err = os.Stat(process.ready); err == nil {
						break
					}
					if !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
						t.Fatalf("child failed to reach write barrier: %v", err)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err = os.WriteFile(gate, []byte("go"), 0600); err != nil {
				t.Fatal(err)
			}
			var created, reused, conflicted, retries int
			var winner runrelation.Relation
			for _, process := range processes {
				result := awaitRelationProcess(t, process, 0)
				retries += result.BusyRetries
				switch result.Disposition {
				case "created":
					created++
					winner = result.Relation
				case "reused":
					reused++
				case "conflict":
					conflicted++
				default:
					t.Fatalf("unexpected child outcome: %+v", result)
				}
			}
			if created != 1 || reused != 1 || conflicted != 2 {
				t.Fatalf("cross-process outcomes created=%d reused=%d conflict=%d busyRetries=%d",
					created, reused, conflicted, retries)
			}
			reopened, err := sqlite.OpenExisting(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reopened.Close() }()
			all, err := sqlite.NewRunRelationStore(reopened).ListAll(t.Context())
			if err != nil || len(all) != 1 || !runrelation.EqualIdentity(all[0], winner) {
				t.Fatalf("cross-process uniqueness lost: %+v winner=%+v err=%v", all, winner, err)
			}
		})
	}
}

func TestSQLiteRunRelationUncommittedCrashRollsBackAcrossProcesses(t *testing.T) {
	base := privateDir(t)
	path := filepath.Join(base, "relations.sqlite")
	store, err := sqlite.CreateNew(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	crash := launchRelationProcess(t, path, "tx-crash", "parent", "child", "owner", "", "crash")
	awaitRelationProcess(t, crash, 84)
	reopened, err := sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	relations := sqlite.NewRunRelationStore(reopened)
	if _, err = relations.GetByChild(t.Context(), "child"); !errors.Is(err, runrelation.ErrNotFound) {
		t.Fatalf("uncommitted row survived crash: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	retry := launchRelationProcess(t, path, "put", "parent", "child", "owner", "", "retry")
	result := awaitRelationProcess(t, retry, 0)
	if result.Disposition != "created" {
		t.Fatalf("new process could not commit after uncommitted crash: %+v", result)
	}
	reopened, err = sqlite.OpenExisting(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	existing, err := sqlite.NewRunRelationStore(reopened).GetByChild(t.Context(), "child")
	if err != nil || !runrelation.EqualIdentity(existing, result.Relation) {
		t.Fatalf("crash/retry durable result=%+v err=%v", existing, err)
	}
}
