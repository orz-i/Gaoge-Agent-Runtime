package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	redisadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis"
	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type hostedSignalModel struct {
	calls atomic.Int32
}

func (m *hostedSignalModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{
		ToolCalls: []tools.Call{{ID: "hosted-call", ToolKey: "test.external", Arguments: json.RawMessage("{}")}},
		Usage:     &model.Usage{InputTokens: 1, OutputTokens: 1},
	}, nil
}

// These records are owned by the TEST HOST, not by the SDK. No host inbox,
// provider journal, or service endpoint is added to the production runtime.
type hostedProviderRecord struct {
	RunID     string
	CallID    string
	Committed bool
}

func syncHostFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func readHostSignal(path string) (agent.ExternalToolResultSignal, error) {
	var signal agent.ExternalToolResultSignal
	data, err := os.ReadFile(path)
	if err != nil {
		return signal, err
	}
	err = json.Unmarshal(data, &signal)
	return signal, err
}

// Test host-side recovery with real PostgreSQL and Redis adapters. The
// test-owned file models a host's durable notification source across
// reconstructed clients, not an implemented production Hosted endpoint.
// An early signal survives negative acknowledgment and duplicate delivery;
// the Worker NACKs unverified work and later ACKs only a provider-proven COMMIT.
func TestRealPostgresRedisNativeEarlySignalProviderRecovery(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if dsn == "" || address == "" {
		t.Skip("requires disposable PostgreSQL and Redis test services")
	}
	ctx := t.Context()
	id := "hosted-signal-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	dir := t.TempDir()
	hostOutbox := filepath.Join(dir, "host-ready-signal.json")
	providerJournal := filepath.Join(dir, "provider-record.json")
	effectsFile := filepath.Join(dir, "physical-effects")
	digest := strings.Repeat("c", 64)
	signal := agent.ExternalToolResultSignal{
		RunID: id, CallID: "hosted-call", OperationID: "provider-operation",
		InputDigest: digest, SignalID: "provider-notification-1",
	}
	provider := hostedProviderRecord{RunID: id, CallID: signal.CallID}
	recordProvider := func() error {
		data, err := json.Marshal(provider)
		if err != nil {
			return err
		}
		return syncHostFile(providerJournal, data)
	}
	openDB := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			sqlDB, e := db.DB()
			if e == nil {
				_ = sqlDB.Close()
			}
		})
		return db
	}
	db := openDB()
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	modelClient := &hostedSignalModel{}
	var firstRunner *agent.Runner
	var earlyErr error
	var earlyRevision uint64
	buildRunner := func(db *gorm.DB, owner string) (*agent.Runner, *kernel.Runtime) {
		t.Helper()
		rt, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db)})
		if err != nil {
			t.Fatal(err)
		}
		registry, err := tools.NewRegistry([]tools.Registration{{
			Definition: tools.Definition{
				Key: "test.external", Name: "External", Terminal: true,
				InputSchema: json.RawMessage("{\"type\":\"object\"}"),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				// The provider-side effect is written before the notification.
				file, err := os.OpenFile(effectsFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					return tools.ExecutionResult{}, err
				}
				_, err = fmt.Fprintln(file, request.Call.ID)
				if err == nil {
					err = file.Sync()
				}
				if err = errors.Join(err, file.Close()); err != nil {
					return tools.ExecutionResult{}, err
				}
				if err = recordProvider(); err != nil {
					return tools.ExecutionResult{}, err
				}
				if data, marshalErr := json.Marshal(signal); marshalErr != nil {
					return tools.ExecutionResult{}, marshalErr
				} else if err = syncHostFile(hostOutbox, data); err != nil {
					return tools.ExecutionResult{}, err
				}
				early, notifyErr := firstRunner.NotifyExternalToolResultReady(ctx, signal)
				earlyErr = notifyErr
				earlyRevision = early.Run.Revision
				if !errors.Is(notifyErr, agent.ErrToolSignalNotReady) {
					return tools.ExecutionResult{}, fmt.Errorf("early signal incorrectly acknowledged: %w", notifyErr)
				}
				deadline := time.Now().UTC().Add(2 * time.Minute)
				return tools.ExecutionResult{
					Content: json.RawMessage("null"),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: tools.ReceiptDispositionPending},
					Pending: &tools.PendingExecution{
						Kind: tools.PendingExternalResult, OperationID: signal.OperationID,
						InputDigest: digest, DeadlineAt: &deadline,
					},
				}, nil
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: modelClient, Catalog: registry, Executor: registry,
			RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 2 * time.Minute},
			ToolOutcomeVerifier: agent.ToolOutcomeVerifierFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				data, err := os.ReadFile(providerJournal)
				if err != nil {
					return tools.ExecutionResult{}, err
				}
				var recorded hostedProviderRecord
				if err = json.Unmarshal(data, &recorded); err != nil {
					return tools.ExecutionResult{}, err
				}
				if request.RunID != recorded.RunID || request.Call.ID != recorded.CallID || !recorded.Committed {
					return tools.ExecutionResult{}, errors.New("provider journal has no committed result for this run and call")
				}
				return tools.ExecutionResult{
					Content: json.RawMessage("{\"ok\":true}"),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
				}, nil
			}),
		})
		if err != nil {
			t.Fatal(err)
		}
		return runner, rt
	}
	firstRunner, _ = buildRunner(db, "starter")
	started, err := firstRunner.StartRun(ctx, agent.StartRequest{
		ID: id, Actor: kernel.ActorRef{TenantID: "hosted-tenant", ActorID: "hosted-actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "hosted-thread"}, Goal: "wait for provider", ToolKeys: []string{"test.external"},
	})
	if err != nil || started.Run.Status != kernel.RunStatusRunning || !errors.Is(earlyErr, agent.ErrToolSignalNotReady) ||
		earlyRevision == 0 || earlyRevision >= started.Run.Revision {
		t.Fatalf("early signal not retained as NACK: status=%s err=%v early=%v rev=%d pendingRev=%d",
			started.Run.Status, err, earlyErr, earlyRevision, started.Run.Revision)
	}
	pending, err := firstRunner.QueryPendingTool(ctx, id, signal.CallID)
	if err != nil || pending.Kind != tools.PendingExternalResult || pending.Generation == 0 {
		t.Fatalf("ToolWait did not become durable: wait=%+v err=%v", pending, err)
	}
	if _, err = os.Stat(hostOutbox); err != nil {
		t.Fatalf("host lost early delivery before ACK: %v", err)
	}

	// Reconstruct the notifier after the starter has returned. Only provider
	// and host-owned durable files plus the PostgreSQL state survive.
	notifier, _ := buildRunner(openDB(), "notifier")
	original, err := readHostSignal(hostOutbox)
	if err != nil {
		t.Fatal(err)
	}
	wrong := original
	wrong.CallID = "different"
	if _, err = notifier.NotifyExternalToolResultReady(ctx, wrong); !errors.Is(err, agent.ErrToolSignalConflict) {
		t.Fatalf("incorrect call accepted as a ready result: %v", err)
	}
	notified, err := notifier.NotifyExternalToolResultReady(ctx, original)
	if err != nil || notified.Run.Revision != started.Run.Revision+1 {
		t.Fatalf("host notification did not CAS once: status=%s err=%v", notified.Run.Status, err)
	}

	// Crash after Run CAS, before ACK of the independent host source: the
	// identical signal must be replayable without a second Run revision.
	duplicateRunner, _ := buildRunner(openDB(), "notifier-after-crash")
	replay, err := readHostSignal(hostOutbox)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := duplicateRunner.NotifyExternalToolResultReady(ctx, replay)
	if err != nil || duplicate.Run.Revision != notified.Run.Revision {
		t.Fatalf("host crash-after-CAS replay was not idempotent: status=%s rev=%d err=%v",
			duplicate.Run.Status, duplicate.Run.Revision, err)
	}
	if err = os.Remove(hostOutbox); err != nil {
		t.Fatalf("host could not ACK durable signal source: %v", err)
	}

	client := goredis.NewClient(&goredis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	queue := redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: "stage2c:" + id})
	relations, err := runrelation.New(postgresadapter.NewRunRelationStore(db), nil)
	if err != nil {
		t.Fatal(err)
	}
	projectorRuntime, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	projector, err := continuation.NewScheduler(continuation.SchedulerDependencies{
		Outbox: postgresadapter.NewKernelStore(db), Queue: queue, Relations: relations,
		Runs:                projectorRuntime,
		RequireDurableQueue: true, ProjectorID: id + "-projector",
	}, continuationadapter.Triggers()...)
	if err != nil {
		t.Fatal(err)
	}
	if err = projector.Project(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := queue.List(ctx, continuation.QueueName, "")
	if err != nil {
		t.Fatal(err)
	}
	var readyJob queuecore.Job
	for _, job := range jobs {
		var payload continuation.Payload
		if err = json.Unmarshal(job.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Trigger == continuation.TriggerToolResultReady && payload.WaitGeneration == pending.Generation {
			readyJob = job
		}
	}
	if readyJob.ID == "" {
		t.Fatalf("committed ReadyWork not projected to Redis: %+v", jobs)
	}
	// First worker sees the signal, but the separate provider-owned journal
	// still says Pending: the delivery must NACK and not call Tool.Execute.
	unverified, runtime1 := buildRunner(openDB(), "worker-unverified")
	dispatcher, err := continuation.NewDispatcher(runtime1, continuation.RegisterResumer(agent.RunKind, unverified))
	if err != nil {
		t.Fatal(err)
	}
	reports := make(chan error, 8)
	worker, err := continuation.NewWorker(queue, dispatcher, continuation.WorkerOptions{
		WorkerID: id + "-worker-a", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
		Report: func(err error) {
			select {
			case reports <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	workerCtx, cancelWorker := context.WithCancel(ctx)
	if err = worker.Start(workerCtx); err != nil {
		t.Fatal(err)
	}
	select {
	case dispatchErr := <-reports:
		if !errors.Is(dispatchErr, agent.ErrToolOutcomeUnknown) {
			t.Fatalf("unverified external result did not NACK: %v", dispatchErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Redis Worker did not NACK unverified result")
	}
	cancelWorker()
	if err = worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	nacked, err := queue.Get(ctx, readyJob.ID)
	if err != nil || nacked.Status != queuecore.StatusQueued || nacked.Attempt != 1 ||
		nacked.LastErrorCode != "continuation.dispatch_failed" {
		t.Fatalf("unverified job was ACKed or disappeared: job=%+v err=%v", nacked, err)
	}
	unresolved, err := runtime1.Load(ctx, id)
	if err != nil || unresolved.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("NACK changed unresolved Run status: status=%s err=%v", unresolved.Run.Status, err)
	}
	unresolvedWait, err := unverified.QueryPendingTool(ctx, id, signal.CallID)
	if err != nil || unresolvedWait.SignalID != signal.SignalID {
		t.Fatalf("NACK consumed pending external receipt: wait=%+v err=%v", unresolvedWait, err)
	}

	// A later provider COMMIT survives another worker restart; Redis redelivers
	// the same job, and only the trusted lookup can finish the Native Run.
	provider.Committed = true
	if err = recordProvider(); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	restartedClient := goredis.NewClient(&goredis.Options{Addr: address})
	defer func() { _ = restartedClient.Close() }()
	restartedQueue := redisadapter.NewQueue(restartedClient, redisadapter.QueueOptions{KeyPrefix: "stage2c:" + id})
	verified, runtime2 := buildRunner(openDB(), "worker-verified")
	restartedDispatcher, err := continuation.NewDispatcher(runtime2, continuation.RegisterResumer(agent.RunKind, verified))
	if err != nil {
		t.Fatal(err)
	}
	restartedWorker, err := continuation.NewWorker(restartedQueue, restartedDispatcher, continuation.WorkerOptions{
		WorkerID: id + "-worker-b", PollInterval: 5 * time.Millisecond, ClaimLimit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	restartedCtx, stopRestarted := context.WithCancel(ctx)
	defer stopRestarted()
	if err = restartedWorker.Start(restartedCtx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		latest, getErr := restartedQueue.Get(ctx, readyJob.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if latest.Status == queuecore.StatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stopRestarted()
	if err = restartedWorker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	completedJob, err := restartedQueue.Get(ctx, readyJob.ID)
	if err != nil || completedJob.Status != queuecore.StatusCompleted || completedJob.Attempt != 2 {
		t.Fatalf("verified result not ACKed on redelivery: job=%+v err=%v", completedJob, err)
	}
	final, err := runtime2.Load(ctx, id)
	if err != nil || final.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("verified external result did not complete durable Run: status=%s err=%v", final.Run.Status, err)
	}
	physical, err := os.ReadFile(effectsFile)
	if err != nil || strings.Count(string(physical), signal.CallID) != 1 || modelClient.calls.Load() != 1 {
		t.Fatalf("provider effect was replayed: effects=%q modelCalls=%d err=%v",
			physical, modelClient.calls.Load(), err)
	}
}
