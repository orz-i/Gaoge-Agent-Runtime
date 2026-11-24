package acp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type independentCreateAcknowledgementFence struct {
	kernel.Store
	created chan kernel.Snapshot
	release chan struct{}
}

func (s *independentCreateAcknowledgementFence) Create(ctx context.Context, r kernel.Record, ev []kernel.EventDraft) (kernel.Snapshot, error) {
	snap, err := s.Store.Create(ctx, r, ev)
	if err == nil && snap.Run.Kind == RunKind {
		s.created <- snap
		<-s.release
	}
	return snap, err
}

func TestPR151CancelBeforeOriginalCreateAck(t *testing.T) {
	f := &fakeClient{}
	store := &independentCreateAcknowledgementFence{Store: memory.NewStore(), created: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: f}})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, e := runner.StartRun(context.Background(), startRequest("create-ack-after-cancel"))
		done <- outcome{snap, e}
	}()
	original := <-store.created
	before, err := runner.CancelRun(t.Context(), original.Run.ID, original.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("expected cancellation pending before Session ID; got %+v %v", before.Run, err)
	}
	o, p, c := f.counts()
	if o != 0 || p != 0 || c != 0 {
		t.Fatalf("client effect before releasing Create ack: %d %d %d", o, p, c)
	}
	close(store.release)
	got := <-done
	o, p, c = f.counts()
	persisted, err := rt.Load(t.Context(), original.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("CancelRun returned before original Create acknowledgement: before revision=%d, start err=%v; after release opens=%d prompts=%d cancels=%d; persisted=%s/%d", before.Run.Revision, got.err, o, p, c, persisted.Run.Status, persisted.Run.Revision)
	if o != 0 {
		t.Fatal("new ACP session/new physically dispatched AFTER original CancelRun durably returned")
	}
}

func TestPR151CallerCancelledBeforeCreateAck(t *testing.T) {
	f := &fakeClient{}
	store := &independentCreateAcknowledgementFence{Store: memory.NewStore(), created: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: f}})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, e := runner.StartRun(ctx, startRequest("create-ack-after-ctx-cancel"))
		done <- outcome{snap, e}
	}()
	<-store.created
	cancel()
	close(store.release)
	got := <-done
	o, p, c := f.counts()
	t.Logf("cancelled request before Create ACK: start err=%v opens=%d prompts=%d cancels=%d", got.err, o, p, c)
	if o != 0 {
		t.Fatal("new ACP session/new physically dispatched on cancelled request after delayed Create ACK")
	}
}

type independentRealACPClient struct {
	inner     *StdioClient
	openCalls int
	sessions  int
}

func (c *independentRealACPClient) OpenSession(ctx context.Context, runID, cwd string) (string, error) {
	c.openCalls++
	id, err := c.inner.OpenSession(ctx, runID, cwd)
	if id != "" && err == nil {
		c.sessions++
	}
	return id, err
}
func (c *independentRealACPClient) Prompt(ctx context.Context, runID, session, goal string, onChunk func(string) error) (string, error) {
	return c.inner.Prompt(ctx, runID, session, goal, onChunk)
}
func (c *independentRealACPClient) CancelSession(ctx context.Context, runID, session string) error {
	return c.inner.CancelSession(ctx, runID, session)
}
func (c *independentRealACPClient) AbandonRun(id string) { c.inner.AbandonRun(id) }
func TestPR151RealStdioACPFirstSessionAfterCancel(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "sdkagent")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent").CombinedOutput()
	if err != nil {
		t.Fatalf("build peer: %v %s", err, out)
	}
	cwd := t.TempDir()
	std, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
	if err != nil {
		t.Fatal(err)
	}
	client := &independentRealACPClient{inner: std}
	store := &independentCreateAcknowledgementFence{Store: memory.NewStore(), created: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: cwd, Client: client}})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, e := runner.StartRun(context.Background(), startRequest("sdk-late-create-ack"))
		done <- outcome{snap, e}
	}()
	original := <-store.created
	before, err := runner.CancelRun(t.Context(), original.Run.ID, original.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("no durable cancellation: %+v %v", before.Run, err)
	}
	close(store.release)
	got := <-done
	t.Logf("first-party SDK ACP opened sessions AFTER durable cancellation returned: OpenSession=%d completed Session/New=%d, StartErr=%v", client.openCalls, client.sessions, got.err)
	if client.sessions != 0 {
		t.Fatal("real ACP SDK completed new external session after CancelRun already returned")
	}
}

// A first-dispatch CAS may be committed before its acknowledgement arrives.
// The original caller cannot treat that delayed acknowledgement as a renewed
// grant after cancellation committed a later revision.
type independentOpenAdmissionACKFence struct {
	kernel.Store
	committed chan kernel.Snapshot
	release   chan struct{}
}

func (s *independentOpenAdmissionACKFence) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	snap, err := s.Store.Apply(ctx, id, rev, m)
	if err == nil && rev == 1 && len(m.Events) != 0 && m.Events[0].Type == "acp.remote_open_dispatch_admitted" {
		s.committed <- snap
		<-s.release
	}
	return snap, err
}

func TestPR151ACPDelayedFirstDispatchClaimCannotOutliveCancel(t *testing.T) {
	f := &fakeClient{}
	store := &independentOpenAdmissionACKFence{Store: memory.NewStore(), committed: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{
		Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)),
		Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: f},
	})
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, startErr := runner.StartRun(context.Background(), startRequest("claim-ack-after-cancel"))
		done <- outcome{snap, startErr}
	}()
	admitted := <-store.committed
	cancelled, cancelErr := runner.CancelRun(t.Context(), admitted.Run.ID, admitted.Run.Revision)
	if !errors.Is(cancelErr, ErrOutcomeUnknown) || cancelled.Run.Revision != admitted.Run.Revision+1 {
		close(store.release)
		t.Fatalf("original cancellation was not serialized after first-dispatch claim: %+v %v", cancelled.Run, cancelErr)
	}
	close(store.release)
	result := <-done
	opened, prompted, cancelledCalls := f.counts()
	if opened != 0 || prompted != 0 || cancelledCalls != 0 || !errors.Is(result.err, ErrOutcomeUnknown) {
		t.Fatalf("delayed first-dispatch claim escaped committed cancel: opens=%d prompts=%d cancels=%d err=%v",
			opened, prompted, cancelledCalls, result.err)
	}
}
