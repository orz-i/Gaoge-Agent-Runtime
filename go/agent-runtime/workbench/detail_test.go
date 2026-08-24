package workbench_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const (
	providerWorkflow = "workflow"
	providerContext  = "context"
)

var errProviderFailed = errors.New("provider failed")

func TestQueryBuildsStableSectionsAndBaseDetail(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: providerWorkflow, content: json.RawMessage(`{"waits":1,"budget":{"used":2}}`)}},
		{Provider: staticProvider{name: providerContext, content: json.RawMessage(`{ "revision": 2, "artifacts": 3 }`)}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get workbench detail: %v", err)
	}
	assertWorkbenchOverview(t, snapshot, detail)
	assertWorkbenchSections(t, detail)
	assertWorkbenchIdentity(t, detail)
	second, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil || second.Hash != detail.Hash {
		t.Fatalf("detail hash is unstable: first=%s second=%s err=%v", detail.Hash, second.Hash, err)
	}
}

func assertWorkbenchOverview(t *testing.T, snapshot kernel.Snapshot, detail workbench.Detail) {
	t.Helper()
	if detail.Overview.RunID != snapshot.Run.ID || detail.Overview.EventCount != 2 ||
		!detail.Overview.HasCheckpoint || detail.Overview.HasResult {
		t.Fatalf("unexpected overview: %#v", detail.Overview)
	}
}

func assertWorkbenchSections(t *testing.T, detail workbench.Detail) {
	t.Helper()
	if len(detail.Sections) != 2 || detail.Sections[0].Name != providerContext ||
		detail.Sections[1].Name != providerWorkflow {
		t.Fatalf("section order is unstable: %#v", detail.Sections)
	}
	for _, section := range detail.Sections {
		if !section.Available || section.Hash == "" || !json.Valid(section.Content) {
			t.Fatalf("invalid section: %#v", section)
		}
	}
}

func assertWorkbenchIdentity(t *testing.T, detail workbench.Detail) {
	t.Helper()
	if detail.Hash == "" || len(detail.Timeline) != 3 {
		t.Fatalf("missing detail identity or base timeline: %#v", detail)
	}
}

func TestQueryKeepsBaseDetailWhenOptionalProvidersFail(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "missing", available: false}},
		{Provider: staticProvider{name: "unavailable", sectionErr: workbench.ErrUnavailable, timelineErr: workbench.ErrUnavailable}},
		{Provider: staticProvider{name: "broken", sectionErr: errProviderFailed, timelineErr: errProviderFailed}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("optional provider failure broke base detail: %v", err)
	}
	if detail.Run.ID != snapshot.Run.ID || len(detail.Sections) != 3 || len(detail.Diagnostics) != 4 {
		t.Fatalf("unexpected degraded detail: %#v", detail)
	}
	for _, section := range detail.Sections {
		if section.Available {
			t.Fatalf("failed or missing section reported available: %#v", section)
		}
	}
}

func TestQueryRejectsDuplicateProviderNames(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	_, err := workbench.NewQuery(fakeRunSource{snapshot: snapshot}, []workbench.Registration{
		{Provider: staticProvider{name: providerWorkflow}},
		{Provider: staticProvider{name: " " + providerWorkflow + " "}},
	})
	if !errors.Is(err, workbench.ErrInvalidInput) {
		t.Fatalf("expected duplicate provider rejection, got %v", err)
	}
}

func TestQueryIsSafeForConcurrentReuse(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "plan", content: json.RawMessage(`{"steps":2}`)}},
	})
	const workers = 16
	results := make(chan workbench.Detail, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			detail, err := query.Get(context.Background(), snapshot.Run.ID)
			results <- detail
			errorsChannel <- err
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent query failed: %v", err)
		}
	}
	var hash string
	for detail := range results {
		if hash == "" {
			hash = detail.Hash
			continue
		}
		if detail.Hash != hash {
			t.Fatalf("concurrent detail hash changed: %s != %s", detail.Hash, hash)
		}
	}
}

func mustQuery(
	t *testing.T,
	snapshot kernel.Snapshot,
	registrations []workbench.Registration,
) *workbench.Query {
	t.Helper()
	query, err := workbench.NewQuery(fakeRunSource{snapshot: snapshot}, registrations)
	if err != nil {
		t.Fatalf("create workbench query: %v", err)
	}
	return query
}

func baseSnapshot() kernel.Snapshot {
	createdAt := time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)
	return kernel.Snapshot{
		Run: kernel.Run{
			ID: "run_1", Kind: kernel.RunKind("workbench_test"),
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "execute", Status: kernel.RunStatusWaitingInput, Revision: 3,
			CreatedAt: createdAt, UpdatedAt: createdAt.Add(3 * time.Second),
		},
		State: json.RawMessage(`{"node":2}`),
		Checkpoint: &kernel.Checkpoint{
			ID: "wait_1", Kind: "workflow_wait", Status: kernel.CheckpointPending,
			Payload: json.RawMessage(`{"kind":"approval"}`), CreatedAt: createdAt.Add(2 * time.Second),
		},
		Events: []kernel.Event{
			{Seq: 1, Type: "workflow.started", CreatedAt: createdAt},
			{Seq: 2, Type: "workflow.wait.created", Message: "wait_1", CreatedAt: createdAt.Add(time.Second)},
		},
	}
}

type fakeRunSource struct {
	snapshot kernel.Snapshot
}

func (source fakeRunSource) Load(_ context.Context, runID string) (kernel.Snapshot, error) {
	if runID != source.snapshot.Run.ID {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return source.snapshot, nil
}

type staticProvider struct {
	name        string
	content     json.RawMessage
	available   bool
	sectionErr  error
	timeline    []workbench.TimelineItem
	timelineErr error
}

func (provider staticProvider) Name() string { return provider.name }

func (provider staticProvider) Section(
	context.Context,
	kernel.Snapshot,
) (json.RawMessage, bool, error) {
	available := provider.available || len(provider.content) > 0
	return provider.content, available, provider.sectionErr
}

func (provider staticProvider) Timeline(
	context.Context,
	kernel.Snapshot,
) ([]workbench.TimelineItem, error) {
	return append([]workbench.TimelineItem(nil), provider.timeline...), provider.timelineErr
}
