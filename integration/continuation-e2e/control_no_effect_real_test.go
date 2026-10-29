package continuatione2e

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/gorm"
)

// This failure occurs on the native Agent's second Kernel read, after the
// Harness control grant commits and BEFORE Agent ApplyControl. The first read
// belongs to the Harness's preliminary run inspection.
type realControlReadFault struct {
	*postgresadapter.KernelStore
	armed  atomic.Bool
	reads  atomic.Int32
	failAt atomic.Int32
}

var errRealControlReadFault = errors.New("injected real PostgreSQL read failure")

func (store *realControlReadFault) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if store.armed.Load() {
		read := store.reads.Add(1)
		failureAt := store.failAt.Load()
		if failureAt == 0 {
			failureAt = 2 // Existing native Agent preflight failure.
		}
		if read == failureAt {
			return kernel.Snapshot{}, errRealControlReadFault
		}
	}
	return store.KernelStore.Load(ctx, runID)
}

type realControlResponseModel struct{}

func (realControlResponseModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "completed after control"}, nil
}

type realControlPauseOnce struct{ checks int }

func (policy *realControlPauseOnce) ValidateCompletion(context.Context, kernel.Run, model.Response) (agent.CompletionCorrection, error) {
	policy.checks++
	if policy.checks == 1 {
		return agent.CompletionCorrection{Code: "retry_answer", Message: "require another model call"}, nil
	}
	return agent.CompletionCorrection{}, nil
}

func newRealControlHost(t *testing.T, db *gorm.DB, fault *realControlReadFault) (*harness.Runner, *harnesspostgres.Store, *kernel.Runtime) {
	t.Helper()
	hostStore, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	var kernelStore kernel.Store = postgresadapter.NewKernelStore(db)
	if fault != nil {
		kernelStore = fault
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: kernelStore})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: realControlResponseModel{},
		CompletionPolicies: []agent.CompletionPolicy{&realControlPauseOnce{}},
		Limits:             agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: direct, Store: hostStore, Clock: managedRecoveryClock{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return host, hostStore, runtime
}

// A different PostgreSQL connection (no in-memory Harness or Kernel state)
// must safely continue a native Agent after the first connection suffers
// a pre-effect read failure. The failure receipt is stored in Harness Items.
func TestRealPostgresNativeControlNoEffectRecoveryAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, recovery := range []string{"retry", "cancel"} {
		t.Run(recovery, func(t *testing.T) {
			db, isolated := managedRecoveryDatabase(t, dsn)
			if err := postgresadapter.Migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			fault := &realControlReadFault{KernelStore: postgresadapter.NewKernelStore(db)}
			first, _, _ := newRealControlHost(t, db, fault)
			request := harness.StartRequest{
				HostThread: harness.HostRef{Kind: "conversation", ID: "control-preflight-recovery"},
				HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "preflight-run"},
				Actor:      kernel.ActorRef{TenantID: "test", ActorID: "agent"},
				Thread:     kernel.ThreadRef{Kind: "conversation", ID: "control-preflight-recovery"},
				Goal:       "produce a second answer", Config: harness.ConfigSnapshot{Model: "fixture-control"},
				CallAllowance: &agent.CallAllowance{LLMCalls: 1},
			}
			paused, err := first.Start(t.Context(), request)
			if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
				t.Fatalf("initial Run not paused: status=%s err=%v", paused.Turn.Status, err)
			}
			owner, ok := harness.TopLevelInvocation(paused)
			if !ok {
				t.Fatal("missing root Invocation")
			}
			fault.armed.Store(true)
			_, failure := first.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
			fault.armed.Store(false)
			if fault.reads.Load() != 2 || !errors.Is(failure, errRealControlReadFault) ||
				!errors.Is(failure, agent.ErrControlNoEffect) {
				t.Fatalf("first instance did not persist no-effect proof: reads=%d err=%v", fault.reads.Load(), failure)
			}

			// A new connection/Store/Runtime/Agent sees only committed SQL.
			secondDB := openManagedRecoveryDB(t, isolated)
			second, secondStore, secondRuntime := newRealControlHost(t, secondDB, nil)
			items, err := secondStore.ListItems(t.Context(), paused.Turn.ID, 0, 500)
			if err != nil {
				t.Fatal(err)
			}
			if harness.PendingExecutionDispatch(items, owner.ID) {
				t.Fatal("independent instance observed a stranded physical-dispatch grant")
			}
			switch recovery {
			case "retry":
				resumed, resumeErr := second.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
				if resumeErr != nil || resumed.Turn.Status != harness.TurnRunning {
					t.Fatalf("new PostgreSQL connection could not grant after proven no-effect: %s %v", resumed.Turn.Status, resumeErr)
				}
			case "cancel":
				cancelled, cancelErr := second.Cancel(t.Context(), paused.Turn.ID, "operator after transient read failure")
				if cancelErr != nil || cancelled.Turn.Status != harness.TurnCancelled {
					t.Fatalf("new PostgreSQL connection could not cancel: %s %v", cancelled.Turn.Status, cancelErr)
				}
			}
			actual, err := secondRuntime.Load(t.Context(), owner.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			expected := kernel.RunStatusRunning
			if recovery == "cancel" {
				expected = kernel.RunStatusCancelled
			}
			if actual.Run.Status != expected {
				t.Fatalf("unexpected authoritative Kernel status: %s want %s", actual.Run.Status, expected)
			}
		})
	}
}

// This helper runs in a separate OS process, creates/settles its own
// persisted control grant, and exits WITHOUT deferred cleanup. The parent
// then recovers using a fresh connection/Runtime/Agent.
func TestRealPostgresNativeControlNoEffectProcess(t *testing.T) {
	if os.Getenv("GAOGE_TEST_NATIVE_CONTROL_PRE_DISPATCH") != "inject" {
		t.Skip("subprocess helper")
	}
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	turnID := os.Getenv("GAOGE_TEST_NATIVE_CONTROL_TURN_ID")
	if dsn == "" || turnID == "" {
		t.Fatal("missing subprocess recovery input")
	}
	db := openManagedRecoveryDB(t, dsn)
	fault := &realControlReadFault{KernelStore: postgresadapter.NewKernelStore(db)}
	if requested := os.Getenv("GAOGE_TEST_NATIVE_CONTROL_FAIL_AT"); requested != "" {
		failAt, err := strconv.Atoi(requested)
		if err != nil || (failAt != 2 && failAt != 3) {
			t.Fatalf("unsupported Kernel read injection point: %q", requested)
		}
		fault.failAt.Store(int32(failAt))
	}
	runner, store, _ := newRealControlHost(t, db, fault)
	turn, err := store.GetTurn(t.Context(), turnID)
	if err != nil || turn.Status != harness.TurnPausedBudget {
		t.Fatalf("unexpected recovered Turn: status=%s err=%v", turn.Status, err)
	}
	fault.armed.Store(true)
	_, err = runner.GrantCallAllowance(t.Context(), turn.ID, turn.Revision, agent.CallAllowance{LLMCalls: 1})
	fault.armed.Store(false)
	expectedReads := fault.failAt.Load()
	if expectedReads == 0 {
		expectedReads = 2
	}
	if fault.reads.Load() != expectedReads || !errors.Is(err, errRealControlReadFault) ||
		!errors.Is(err, agent.ErrControlNoEffect) {
		t.Fatalf("expected proven pre-effect control read failure: reads=%d expected=%d err=%v",
			fault.reads.Load(), expectedReads, err)
	}
	items, err := store.ListItems(t.Context(), turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.PendingExecutionDispatch(items, "") {
		t.Fatal("subprocess left a pending native control claim")
	}
	// An abrupt process exit tests cross-process durability, not in-memory
	// runner cleanup. This status is checked exactly by the parent.
	os.Exit(77)
}

func TestRealPostgresNativeControlNoEffectSurvivesProcessCrash(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		failAt int
	}{
		{"agent-preflight-read", 2},
		{"kernel-pre-store-read", 3},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			testRealPostgresNativeControlNoEffectSurvivesProcessCrash(t, scenario.failAt)
		})
	}
}

func testRealPostgresNativeControlNoEffectSurvivesProcessCrash(t *testing.T, failAt int) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolated := managedRecoveryDatabase(t, dsn)
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, _, _ := newRealControlHost(t, db, nil)
	request := harness.StartRequest{
		HostThread: harness.HostRef{Kind: "conversation", ID: "control-process-exit"},
		HostTurn:   harness.HostRef{Kind: "conversation_turn", ID: "recover-after-exit"},
		Actor:      kernel.ActorRef{TenantID: "test", ActorID: "agent"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "control-process-exit"},
		Goal:       "continue after process termination", Config: harness.ConfigSnapshot{Model: "fixture-control"},
		CallAllowance: &agent.CallAllowance{LLMCalls: 1},
	}
	paused, err := first.Start(t.Context(), request)
	if err != nil || paused.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("parent could not create paused Run: status=%s err=%v", paused.Turn.Status, err)
	}
	owner, ok := harness.TopLevelInvocation(paused)
	if !ok {
		t.Fatal("missing persisted Invocation")
	}
	command := exec.Command(os.Args[0], "-test.run=^TestRealPostgresNativeControlNoEffectProcess$", "-test.v")
	command.Env = append(os.Environ(),
		"TEST_POSTGRES_DSN="+isolated,
		"GAOGE_TEST_NATIVE_CONTROL_PRE_DISPATCH=inject",
		"GAOGE_TEST_NATIVE_CONTROL_FAIL_AT="+strconv.Itoa(failAt),
		"GAOGE_TEST_NATIVE_CONTROL_TURN_ID="+paused.Turn.ID,
	)
	output, exitErr := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(exitErr, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("control-preflight subprocess must terminate after recording no-effect: err=%v output=%s", exitErr, output)
	}
	// The new process has no access to the former Harness, Agent or Kernel
	// objects. Its only recovery authority is the committed SQL history.
	db2 := openManagedRecoveryDB(t, isolated)
	second, store2, runtime2 := newRealControlHost(t, db2, nil)
	items, err := store2.ListItems(t.Context(), paused.Turn.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	if harness.PendingExecutionDispatch(items, owner.ID) {
		t.Fatal("persisted control completion was not visible across process exit")
	}
	resumed, err := second.GrantCallAllowance(t.Context(), paused.Turn.ID, paused.Turn.Revision, agent.CallAllowance{LLMCalls: 1})
	if err != nil || resumed.Turn.Status != harness.TurnRunning {
		t.Fatalf("fresh process could not safely continue from native no-effect receipt: %s err=%v", resumed.Turn.Status, err)
	}
	run, err := runtime2.Load(t.Context(), owner.ExecutionRefID)
	if err != nil || run.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("new process did not persist resumed Kernel status: %s err=%v", run.Run.Status, err)
	}
}
