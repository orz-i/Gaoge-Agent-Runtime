package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

var errHostProjectionMissing = errors.New("host projection missing")

type hostProjectionRepairerStub struct {
	requests []agentruntime.RepairTurnRequest
	err      error
}

func (stub *hostProjectionRepairerStub) RepairTurn(_ context.Context, request agentruntime.RepairTurnRequest) (agentruntime.ProjectionWriteResult, error) {
	stub.requests = append(stub.requests, request)
	return agentruntime.ProjectionWriteResult{}, stub.err
}

func TestRepairPendingHostProjectionsMarksSuccessfulRepair(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	repo := New(db, StaticSessions(db))
	row := historicalHostProjectionRun("run_host_repair")
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	repairer := &hostProjectionRepairerStub{}
	if err := repo.RepairPendingHostProjections(context.Background(), repairer, nil); err != nil {
		t.Fatal(err)
	}
	if len(repairer.requests) != 1 || repairer.requests[0].Usage.OutputTokens != 256 {
		t.Fatalf("repair requests = %#v", repairer.requests)
	}
	var repaired models.RunRecord
	if err := db.Where("run_id = ?", row.RunID).Take(&repaired).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.HostProjectionVersion != currentHostProjectionVersion {
		t.Fatalf("host projection version = %d", repaired.HostProjectionVersion)
	}
}

func TestRepairPendingHostProjectionsLeavesFailedRepairPending(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	repo := New(db, StaticSessions(db))
	row := historicalHostProjectionRun("run_host_missing")
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	repairer := &hostProjectionRepairerStub{err: errHostProjectionMissing}
	var warned string
	if err := repo.RepairPendingHostProjections(context.Background(), repairer, func(runID string, _ error) { warned = runID }); err != nil {
		t.Fatal(err)
	}
	var pending models.RunRecord
	if err := db.Where("run_id = ?", row.RunID).Take(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if warned != row.RunID || pending.HostProjectionVersion != 0 {
		t.Fatalf("warning=%q host projection version=%d", warned, pending.HostProjectionVersion)
	}
}

func historicalHostProjectionRun(runID string) models.RunRecord {
	return models.RunRecord{
		RunID: runID, TenantID: "default", ActorID: "1", ThreadKind: expiryTestThreadConversation, ThreadID: "thread",
		InputProjectionKind: projectionKindMessage, InputProjectionID: "input",
		OutputProjectionKind: projectionKindMessage, OutputProjectionID: migrationOutputProjection,
		Status: domain.RunStatusCompleted, StartedAt: time.Now(), OutputTokens: 256,
		StateProjectionVersion: currentStateProjectionVersion,
	}
}
