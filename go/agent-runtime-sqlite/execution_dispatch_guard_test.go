package sqlite_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// A Store CAS guard alone is too late: previously an unbound Runner.Resume
// invoked the actual Tool and only then got ErrExecutionRequired from receipt
// persistence. The model and Tool must not be invoked before authorization.
func TestUnboundFencedResumeDoesNotExecuteToolBeforeCAS(t *testing.T) {
	ctx := t.Context()
	dir := privateDir(t)
	dbPath := filepath.Join(dir, "resume-fence.sqlite")
	marker := filepath.Join(dir, "effects.txt")
	store, err := sqlite.CreateNew(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	fenced := &controlTestFencedCreateStore{Store: store, execution: store}
	starting, err := kernel.New(kernel.Dependencies{Store: fenced})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := controlTestRunner(starting, marker, true)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := initial.StartRun(ctx, agent.StartRequest{
		ID: "fenced-resume-side-effect", Actor: kernel.ActorRef{TenantID: "p", ActorID: "a"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "t"}, Goal: "two calls", Model: "fixture",
		ToolKeys: []string{"publish"},
	})
	if err != nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("failed initial fence fixture %+v %v", waiting.Run, err)
	}
	plain, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	unbound, err := controlTestRunner(plain, marker, true)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := unbound.ResolveApproval(ctx, waiting.Run.ID, waiting.Run.Revision,
		plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
	if err != nil || approved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("failed control %+v %v", approved.Run, err)
	}
	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	_, err = unbound.Resume(ctx, approved.Run.ID, approved.Run.Revision)
	if !errors.Is(err, kernel.ErrExecutionRequired) {
		t.Fatalf("unbound Resume not rejected BEFORE effects: %v", err)
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || strings.Contains(string(after), "tool:") {
		t.Fatalf("unbound Resume produced a physical Tool effect: before=%q after=%q", before, after)
	}
	still, err := plain.Load(ctx, approved.Run.ID)
	if err != nil || still.Run.Revision != approved.Run.Revision {
		t.Fatalf("unbound Resume changed persisted revision %+v %v", still.Run, err)
	}
	lease, err := plain.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: approved.Run.ID, OwnerID: "authorized", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plain.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := controlTestRunner(bound, marker, true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := authorized.Resume(ctx, approved.Run.ID, approved.Run.Revision)
	if err != nil || next.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("legitimate owner could not Resume: %+v %v", next.Run, err)
	}
	finalMarkers, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(finalMarkers), "tool:first-call") != 1 ||
		strings.Count(string(finalMarkers), "tool:second-call") != 0 {
		t.Fatalf("Tool calls executed without separate approvals: %q", finalMarkers)
	}
}
