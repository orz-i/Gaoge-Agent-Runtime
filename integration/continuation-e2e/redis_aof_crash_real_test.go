package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	redisadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-redis"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// SIGKILL is ONLY allowed for the disposable test container explicitly named
// by STAGE4_REDIS_AOF_CONTAINER. Use the Stage 4 AOF Compose override.
// Docker remove/recreate, disk failure, HA failover and a production Host
// principal/provider are outside the tested process-restart contract.
func TestStage4RealRedisAOFCrashRetainsProtectedDeliveryAndPGFence(t *testing.T) {
	containerName := strings.TrimSpace(os.Getenv("STAGE4_REDIS_AOF_CONTAINER"))
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	address := strings.TrimSpace(os.Getenv("TEST_REDIS_ADDR"))
	if containerName == "" || dsn == "" || address == "" {
		t.Skip("requires disposable Docker Redis AOF fixture and PG16")
	}
	// Never SIGKILL an arbitrary Redis instance supplied through env.
	// This one exact project is reserved for the disposable AOF fixture.
	if containerName != "gaoge-stage4-aof-proof-redis-1" {
		t.Fatal("refusing to crash Redis outside the disposable Stage 4 AOF fixture")
	}
	ctx := t.Context()
	// Verify Docker Compose ownership, not only the user-supplied name,
	// before invoking SIGKILL against any running container.
	inspectCtx, inspectCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer inspectCancel()
	labels, inspectErr := exec.CommandContext(inspectCtx, "docker", "inspect", "--format",
		"{{index .Config.Labels \"com.docker.compose.project\"}}:{{index .Config.Labels \"com.docker.compose.service\"}}", containerName).CombinedOutput()
	if inspectErr != nil || strings.TrimSpace(string(labels)) != "gaoge-stage4-aof-proof:redis" {
		t.Fatalf("refusing SIGKILL: Redis has no matching disposable Compose labels: %v %q", inspectErr, labels)
	}
	docker := func(command ...string) {
		t.Helper()
		callCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		output, err := exec.CommandContext(callCtx, "docker", command...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v: %s", command, err, output)
		}
	}
	t.Cleanup(func() {
		callCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = exec.CommandContext(callCtx, "docker", "start", containerName).Run()
	})
	connectDB := func() *gorm.DB {
		t.Helper()
		db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
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
	db := connectDB()
	if err := postgresadapter.Migrate(db); err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db)})
	if err != nil {
		t.Fatal(err)
	}
	id := "stage4-aof-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	run, err := runtime.Create(ctx, kernel.CreateRequest{
		ID: id, Kind: "test.aof", ExecutionRequired: true,
		Actor:  kernel.ActorRef{TenantID: "aof-tenant", ActorID: "aof-actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "aof-thread"},
		Goal:   "recover one protected delivery after Redis process crash",
		State:  json.RawMessage("{}"),
	})
	if err != nil {
		t.Fatal(err)
	}
	connectQueue := func() (*redisadapter.DeliveryQueue, *goredis.Client) {
		t.Helper()
		client := goredis.NewClient(&goredis.Options{Addr: address})
		t.Cleanup(func() { _ = client.Close() })
		if e := client.Ping(ctx).Err(); e != nil {
			t.Fatal(e)
		}
		return redisadapter.NewQueue(client, redisadapter.QueueOptions{KeyPrefix: "stage4-aof:" + id}), client
	}
	queue, client := connectQueue()
	info, err := client.Info(ctx, "persistence").Result()
	if err != nil || !strings.Contains(info, "aof_enabled:1") {
		t.Fatalf("Redis must START with AOF enabled: %v, %q", err, info)
	}
	payload, err := json.Marshal(continuation.Payload{
		SchemaVersion: continuation.SchemaVersion, RunID: id,
		ExpectedRevision: run.Run.Revision, SourceRunID: id,
		SourceRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := queuecore.EnqueueRequest{
		Queue: continuation.QueueName, ClientJobID: "aof-crash:" + id,
		Kind: continuation.JobKind, Payload: payload,
		Policy: queuecore.Policy{RequireTerminalFence: true, MaxAttempts: 1,
			VisibilityTimeout: 500 * time.Millisecond,
			InitialBackoff:    50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond},
	}
	added, err := queue.Enqueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := queue.Claim(ctx, queuecore.ClaimRequest{Queue: continuation.QueueName, WorkerID: "pre-crash-owner", Limit: 1})
	if err != nil || len(old) != 1 || old[0].Job.ID != added.Job.ID {
		t.Fatalf("cannot lease protected Redis delivery before crash: %+v %v", old, err)
	}
	// appendfsync=always and the SAME container filesystem are explicit
	// persistence assumptions. PostgreSQL retains its own Run authority.
	docker("kill", "--signal=KILL", containerName)
	docker("start", containerName)
	readyBy := time.Now().Add(15 * time.Second)
	for {
		probe := goredis.NewClient(&goredis.Options{Addr: address})
		probeErr := probe.Ping(context.Background()).Err()
		_ = probe.Close()
		if probeErr == nil {
			break
		}
		if time.Now().After(readyBy) {
			t.Fatalf("Redis did not restart after SIGKILL: %v", probeErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	restarted, restartedClient := connectQueue()
	postRestartInfo, err := restartedClient.Info(ctx, "persistence").Result()
	if err != nil || !strings.Contains(postRestartInfo, "aof_enabled:1") {
		t.Fatalf("restarted Redis lost AOF config: %v %q", err, postRestartInfo)
	}
	retained, err := restarted.Get(ctx, added.Job.ID)
	if err != nil || retained.ID != added.Job.ID || !retained.Policy.RequireTerminalFence ||
		retained.Attempt != 1 || retained.Status != queuecore.StatusLeased {
		t.Fatalf("AOF process restart lost leased Job/Policy: %+v %v", retained, err)
	}
	persistedRun, err := runtime.Load(ctx, id)
	if err != nil || persistedRun.Run.Revision != run.Run.Revision ||
		persistedRun.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("Redis crash changed PG Run authority: %+v %v", persistedRun.Run, err)
	}
	// The first Claim after an expired lease only sweeps it into a
	// backed-off retry. A second Claim is eligible when its durable due
	// time is reached; a one-shot Claim is not guaranteed to return work.
	time.Sleep(550 * time.Millisecond)
	var current []queuecore.Delivery
	claimBy := time.Now().Add(4 * time.Second)
	for {
		current, err = restarted.Claim(ctx, queuecore.ClaimRequest{
			Queue: continuation.QueueName, WorkerID: "post-crash-owner", Limit: 1,
		})
		if err != nil || len(current) != 0 || time.Now().After(claimBy) {
			break
		}
		time.Sleep(60 * time.Millisecond)
	}
	if err != nil || len(current) != 1 || current[0].Job.ID != old[0].Job.ID ||
		current[0].Lease.ID == old[0].Lease.ID ||
		current[0].Lease.Generation != old[0].Lease.Generation+1 ||
		current[0].Job.Attempt != 2 {
		t.Fatalf("failed protected lease takeover after AOF restart: %+v %v", current, err)
	}
	if _, err = restarted.Ack(ctx, queuecore.LeaseRequest{
		JobID: old[0].Job.ID, LeaseID: old[0].Lease.ID, WorkerID: "pre-crash-owner",
	}); !errors.Is(err, queuecore.ErrLeaseLost) {
		t.Fatalf("pre-crash worker ACKed stale lease: %v", err)
	}
	reconnectedRT, err := kernel.New(kernel.Dependencies{
		Store: postgresadapter.NewKernelStore(connectDB()),
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := reconnectedRT.AcquireExecution(ctx, kernel.ExecutionAcquireRequest{
		RunID: id, OwnerID: "post-crash-owner", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	fenced, err := reconnectedRT.WithExecution(lease)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := fenced.Apply(ctx, id, run.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage("{\"recovered\":true}"),
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage("true")},
	})
	if err != nil || committed.Run.Revision != run.Run.Revision+1 {
		t.Fatalf("PG post-crash execution did not commit: %+v %v", committed.Run, err)
	}
	completed, err := restarted.Ack(ctx, queuecore.LeaseRequest{
		JobID: current[0].Job.ID, LeaseID: current[0].Lease.ID, WorkerID: "post-crash-owner",
	})
	if err != nil || completed.Status != queuecore.StatusCompleted || completed.Attempt != 2 {
		t.Fatalf("Redis ACK did not follow PG success: %+v %v", completed, err)
	}
	// Redis ACK must also survive an abrupt restart, not merely the
	// original leased delivery. This is still one container filesystem.
	docker("kill", "--signal=KILL", containerName)
	docker("start", containerName)
	readyBy = time.Now().Add(15 * time.Second)
	for {
		probe := goredis.NewClient(&goredis.Options{Addr: address})
		probeErr := probe.Ping(context.Background()).Err()
		_ = probe.Close()
		if probeErr == nil {
			break
		}
		if time.Now().After(readyBy) {
			t.Fatalf("Redis did not recover terminal ACK from AOF: %v", probeErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	finalQueue, _ := connectQueue()
	replayed, err := finalQueue.Enqueue(ctx, request)
	if err != nil || !replayed.Reused || replayed.Job.ID != completed.ID ||
		replayed.Job.Status != queuecore.StatusCompleted {
		t.Fatalf("queue lost terminal idempotent identity: %+v %v", replayed, err)
	}
	finalRun, err := reconnectedRT.Load(ctx, id)
	if err != nil || finalRun.Run.Status != kernel.RunStatusCompleted ||
		finalRun.Run.Revision != run.Run.Revision+1 {
		t.Fatalf("Run was replayed instead of committed once: %+v %v", finalRun.Run, err)
	}
}
