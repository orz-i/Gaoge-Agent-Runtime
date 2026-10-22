package harnesspostgres_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type realManagedSessionClock struct{}

func (realManagedSessionClock) Now() time.Time { return time.Now().UTC() }

type realManagedSessionModel struct{ requests []model.Request }

func (client *realManagedSessionModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, model.CloneRequest(request))
	return model.Response{Content: "verified answer"}, nil
}

func TestRealPostgresManagedSessionCompactionAcrossStoreRestart(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	store1, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	secondSQLDB, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondSQLDB.Close() })
	store2, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}

	id := uuid.NewString()
	thread := "managed-" + id
	historic := strings.Repeat("verified old material ", 360)
	seed := func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		entries := []runtimecontext.Entry{
			{ID: "old-u", SourceID: "source-old-u", TurnID: "old", Message: model.Message{Role: model.RoleUser, Content: "old question"}},
			{ID: "old-a", SourceID: "source-old-a", TurnID: "old", Message: model.Message{Role: model.RoleAssistant, Content: historic}},
			{ID: "current-u", SourceID: "source-current-u", TurnID: "first", Message: model.Message{Role: model.RoleUser, Content: "first prompt"}},
		}
		if turn.ID == "second" {
			entries = append(entries, runtimecontext.Entry{ID: "first-a", SourceID: "source-first-a", TurnID: "first", Message: model.Message{Role: model.RoleAssistant, Content: "verified answer"}})
			entries = append(entries, runtimecontext.Entry{ID: "second-u", SourceID: "source-second-u", TurnID: "second", Required: true, Message: model.Message{Role: model.RoleUser, Content: prompt}})
		} else if turn.ID != "first" {
			return nil, harness.ErrNotFound
		}
		if turn.ID == "first" && prompt != "first prompt" {
			return nil, harness.ErrInvalidRequest
		}
		if turn.ID == "first" {
			entries[len(entries)-1].Required = true
		}
		path := make([]string, len(entries))
		for i := range entries {
			path[i] = entries[i].SourceID
		}
		return &harness.ContextSeed{SourcePath: path, Entries: entries}, nil
	}
	options := harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: thread},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: thread},
		Config: harness.ConfigSnapshot{
			Model: "fixture-model", ToolKeys: []string{harness.ContextArtifactToolKey},
			ContextPolicy: runtimecontext.Policy{PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 80},
		},
		Transcript: harness.TranscriptSourceFunc(seed),
	}
	kernelStore := memory.NewStore()
	newClient := func(store harness.Store, client *realManagedSessionModel) *harness.SessionClient {
		t.Helper()
		rt, err := kernel.New(kernel.Dependencies{Store: kernelStore})
		if err != nil {
			t.Fatal(err)
		}
		manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
		reader := harness.NewContextArtifactToolHandler(store, nil)
		registry, err := tools.NewRegistry([]tools.Registration{{
			Definition: tools.Definition{
				Key: harness.ContextArtifactToolKey, Name: "read_context_artifact", Description: "Read sealed context",
				InputSchema: []byte(`{"type":"object","properties":{"artifactID":{"type":"string"}},"required":["artifactID"]}`),
			},
			Handler: reader,
		}})
		if err != nil {
			t.Fatal(err)
		}
		preparer, err := harness.NewManagedContextPreparer(store, manager, func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 2000, ReservedOutputTokens: 128}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		runContext, err := harness.NewContextRunMiddleware(store, nil)
		if err != nil {
			t.Fatal(err)
		}
		direct, err := agent.NewRunner(agent.Dependencies{
			Runtime: rt, Model: client, Catalog: registry, Executor: registry,
			PrepareModelRequest: preparer,
			RunMiddleware:       []plugin.RunMiddleware{runContext},
		})
		if err != nil {
			t.Fatal(err)
		}
		host, err := harness.NewRunner(harness.Dependencies{
			Runtime: rt, Agent: direct, Store: store, Clock: realManagedSessionClock{},
			Context: manager, Catalog: registry,
		})
		if err != nil {
			t.Fatal(err)
		}
		session, err := host.OpenSession(options)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	firstModel := &realManagedSessionModel{}
	first := newClient(store1, firstModel)
	firstTurn, err := first.Prompt(t.Context(), "first", "first prompt")
	if err != nil || firstTurn.Turn.Status != harness.TurnCompleted || firstTurn.Turn.ContextRef.Generation != 2 {
		t.Fatalf("first durable compaction: turn=%+v err=%v", firstTurn.Turn, err)
	}
	checkpoint, err := store1.GetContextCheckpoint(t.Context(), firstTurn.Turn.ContextCheckpointID)
	if err != nil || len(checkpoint.ArtifactIDs) == 0 {
		t.Fatalf("missing postgres checkpoint archive: %+v err=%v", checkpoint, err)
	}
	oldFound := false
	for _, archiveID := range checkpoint.ArtifactIDs {
		artifact, err := store1.GetContextArtifact(t.Context(), archiveID)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(artifact.ContentJSON), "verified old material") {
			oldFound = true
		}
	}
	if !oldFound {
		t.Fatal("exact removed history not persisted in PostgreSQL")
	}

	nextModel := &realManagedSessionModel{}
	reopened := newClient(store2, nextModel)
	prior, err := reopened.Load(t.Context(), "first")
	if err != nil || prior.Turn.ID != firstTurn.Turn.ID {
		t.Fatalf("restart session load: %+v err=%v", prior.Turn, err)
	}
	secondTurn, err := reopened.Prompt(t.Context(), "second", "second prompt")
	if err != nil || secondTurn.Turn.Status != harness.TurnCompleted || len(nextModel.requests) != 1 {
		t.Fatalf("restart followup: %+v err=%v calls=%d", secondTurn.Turn, err, len(nextModel.requests))
	}
	if secondTurn.Session.ID != firstTurn.Session.ID || secondTurn.Turn.ContextRef.Generation < firstTurn.Turn.ContextRef.Generation {
		t.Fatalf("context Session lineage changed: first=%+v second=%+v", firstTurn.Turn.ContextRef, secondTurn.Turn.ContextRef)
	}
	newQuestion := false
	for _, msg := range nextModel.requests[0].Messages {
		if strings.Contains(msg.Content, historic) {
			t.Fatal("archived history was replayed into model after reopening PostgreSQL")
		}
		if msg.Content == "second prompt" {
			newQuestion = true
		}
	}
	if !newQuestion {
		t.Fatal("second prompt missing from resumed model request")
	}
}
