package workbench_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

func TestTimelineMergesAndSortsProviderItems(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	createdAt := snapshot.Run.CreatedAt
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{
			name: providerWorkflow,
			timeline: []workbench.TimelineItem{
				{ID: "activation_2", Kind: "activation.completed", Seq: 4, CreatedAt: createdAt.Add(4 * time.Second)},
				{ID: "activation_1", Kind: "activation.started", Seq: 3, CreatedAt: createdAt.Add(3 * time.Second)},
			},
		}},
		{Provider: staticProvider{
			name: providerContext,
			timeline: []workbench.TimelineItem{
				{ID: "snapshot_2", Kind: "context.managed", CreatedAt: createdAt.Add(1500 * time.Millisecond)},
			},
		}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get timeline: %v", err)
	}
	if len(detail.Timeline) != 6 {
		t.Fatalf("unexpected timeline length: %#v", detail.Timeline)
	}
	expected := []string{
		"kernel:event:1",
		"kernel:event:2",
		providerContext + ":snapshot_2",
		"kernel:checkpoint:wait_1",
		providerWorkflow + ":activation_1",
		providerWorkflow + ":activation_2",
	}
	for index, item := range detail.Timeline {
		identity := item.Source + ":" + item.ID
		if identity != expected[index] {
			t.Fatalf("timeline order mismatch at %d: %s != %s", index, identity, expected[index])
		}
	}
}

func TestTimelineDeduplicatesStableIdentity(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	createdAt := snapshot.Run.CreatedAt.Add(5 * time.Second)
	item := workbench.TimelineItem{
		ID: "plan_1", Kind: "plan.created", Seq: 5, CreatedAt: createdAt,
		Data: json.RawMessage(`{"steps":2}`),
	}
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "plan", timeline: []workbench.TimelineItem{item, item}}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get deduplicated timeline: %v", err)
	}
	if len(detail.Timeline) != 4 || len(detail.Diagnostics) != 0 {
		t.Fatalf("identical timeline item was not deduplicated: %#v %#v", detail.Timeline, detail.Diagnostics)
	}
}

func TestTimelineIdentityConflictKeepsFirstAndRecordsDiagnostic(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	createdAt := snapshot.Run.CreatedAt.Add(5 * time.Second)
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "team", timeline: []workbench.TimelineItem{
			{ID: "handoff_1", Kind: "handoff.completed", Seq: 5, CreatedAt: createdAt, Data: json.RawMessage(`{"result":1}`)},
			{ID: "handoff_1", Kind: "handoff.failed", Seq: 5, CreatedAt: createdAt, Data: json.RawMessage(`{"result":2}`)},
		}}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get conflicted timeline: %v", err)
	}
	if len(detail.Timeline) != 4 || len(detail.Diagnostics) != 1 ||
		detail.Diagnostics[0].Code != "identity_conflict" {
		t.Fatalf("unexpected conflict handling: %#v %#v", detail.Timeline, detail.Diagnostics)
	}
}

func TestTimelineInvalidProviderItemDoesNotBreakBaseDetail(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	query := mustQuery(t, snapshot, []workbench.Registration{
		{Provider: staticProvider{name: "queue", timeline: []workbench.TimelineItem{
			{ID: "job_1", Kind: "queue.leased"},
		}}},
	})
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("invalid provider timeline broke detail: %v", err)
	}
	if len(detail.Timeline) != 3 || len(detail.Diagnostics) != 1 || detail.Diagnostics[0].Code != "invalid_item" {
		t.Fatalf("unexpected invalid item handling: %#v %#v", detail.Timeline, detail.Diagnostics)
	}
}

func TestTimelineIncludesTerminalResult(t *testing.T) {
	t.Parallel()
	snapshot := baseSnapshot()
	snapshot.Run.Status = kernel.RunStatusCompleted
	snapshot.Result = &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"ok":true}`)}
	query := mustQuery(t, snapshot, nil)
	detail, err := query.Get(context.Background(), snapshot.Run.ID)
	if err != nil {
		t.Fatalf("get terminal timeline: %v", err)
	}
	last := detail.Timeline[len(detail.Timeline)-1]
	if last.ID != "result" || last.Kind != "run.result" || last.Status != string(kernel.RunStatusCompleted) {
		t.Fatalf("terminal result missing from timeline: %#v", detail.Timeline)
	}
}
