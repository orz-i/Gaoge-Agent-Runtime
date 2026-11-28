package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// Independently exercise first-party Plugin + Handoff with a genuine signed
// original ACP receipt, then ask for another frozen revision.
func TestPR151ACPRevisionSubstitution(t *testing.T) {
	f := &fakeClient{}
	rt, _ := fixture(t, f)
	p, err := NewPlugin(PluginDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Bindings: BindingResolverFunc(func(_ context.Context, target, revision string) (Binding, error) {
		return Binding{TargetID: target, Revision: revision, Cwd: "/workspace", Client: f}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	c, err := handoff.New(handoff.Config{Remote: p})
	if err != nil {
		t.Fatal(err)
	}
	req := startRequest("child")
	parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "test.parent", Actor: req.Actor, Thread: req.Thread, Goal: "parent", State: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
	}
	d := handoff.Delegation{ID: "delegate", Remote: true, MemberID: "acp:remote", MemberRevision: "r1", ChildRunID: "child", Goal: "remote goal", Status: handoff.StatusQueued}
	orig, err := c.StartOrLoad(t.Context(), parent, d)
	if err != nil || orig.Status != handoff.StatusCompleted {
		t.Fatalf("setup: %+v err=%v", orig, err)
	}

	fixed, err := p.ResolveChild(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	d.MemberRevision = "r2"
	if _, err := p.ResolveChild(t.Context(), d); err == nil {
		t.Fatal("binding revision substituted")
	}
	wrong, err := handoff.New(handoff.Config{Remote: handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) { return fixed, nil })})
	if err != nil {
		t.Fatal(err)
	}
	for _, coordinator := range []*handoff.Coordinator{c, wrong} {
		if got, err := coordinator.Refresh(t.Context(), parent, d); err == nil || got.ID != "" {
			t.Fatalf("wrong revision refreshed: %+v %v", got, err)
		}
		if got, err := coordinator.StartOrLoad(t.Context(), parent, d); err == nil || got.ID != "" {
			t.Fatalf("wrong revision replayed: %+v %v", got, err)
		}
	}
}

type auditPromptCommitFence struct {
	kernel.Store
	committed chan kernel.Snapshot
	release   chan struct{}
}

func (s *auditPromptCommitFence) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease == nil && commitAuth.Control == nil && commitAuth.Recovery == nil {
		snap, _, err := s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
		if err == nil && commitRev == 3 && strings.Contains(string(commitMutation.Record.State), "\"promptPending\":true") {
			s.committed <- snap
			<-s.release
		}
		return snap, false, err
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func TestPR151ACPCancelBeforePhysicalPrompt(t *testing.T) {
	f := &fakeClient{}
	s := &auditPromptCommitFence{Store: memory.NewStore(), committed: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: "/workspace", Client: f}})
	if err != nil {
		t.Fatal(err)
	}
	type ret struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan ret, 1)
	go func() {
		snap, err := runner.StartRun(context.Background(), startRequest("audit-cancel"))
		done <- ret{snap, err}
	}()
	snap := <-s.committed
	beforeO, beforeP, beforeC := f.counts()
	cancelSnap, cancelErr := runner.CancelRun(t.Context(), snap.Run.ID, snap.Run.Revision)
	afterO, afterP, afterC := f.counts()
	t.Logf("committed prompt intent rev=%d before counts=%d/%d/%d; cancel returned rev=%d err=%v counts=%d/%d/%d", snap.Run.Revision, beforeO, beforeP, beforeC, cancelSnap.Run.Revision, cancelErr, afterO, afterP, afterC)
	close(s.release)
	got := <-done
	o, p, c := f.counts()
	t.Logf("after releasing original dispatch: opens=%d prompts=%d cancels=%d status=%s err=%v", o, p, c, got.snap.Run.Status, got.err)
	if !errors.Is(cancelErr, ErrOutcomeUnknown) {
		t.Fatal("unexpected cancel fixture")
	}
	if o != 1 || p != 0 || c != 1 || afterP != 0 || afterC != 1 || !errors.Is(got.err, ErrOutcomeUnknown) || got.snap.Run.Status != kernel.RunStatusRunning {
		t.Fatal("actual session/prompt occurs after session/cancel returned")
	}
}

func TestPR151ACPRealStdioLatePromptAfterCancel(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "sdkagent")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real SDK peer: %v %s", err, output)
	}
	cwd := t.TempDir()
	client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
	if err != nil {
		t.Fatal(err)
	}
	store := &auditPromptCommitFence{Store: memory.NewStore(), committed: make(chan kernel.Snapshot, 1), release: make(chan struct{})}
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, AttestationKey: []byte(strings.Repeat("K", 32)), Binding: Binding{TargetID: "remote", Revision: "r1", Cwd: cwd, Client: client}})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	go func() {
		snap, err := runner.StartRun(ctx, startRequest("real-stdio-cancel"))
		done <- result{snap, err}
	}()
	var snap kernel.Snapshot
	select {
	case snap = <-store.committed:
	case got := <-done:
		t.Fatalf("failed before prompt admission: %v", got.err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancelled, err := runner.CancelRun(ctx, snap.Run.ID, snap.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) {
		close(store.release)
		t.Fatalf("unexpected cancel result %v", err)
	}
	var after remoteState
	if err = decodeRemoteState(cancelled.State, &after); err != nil {
		close(store.release)
		t.Fatal(err)
	}
	t.Logf("before physical Prompt: rev=%d cancelReturned=%t promptReturned=%t", cancelled.Run.Revision, after.CancelReturned, after.PromptReturned)
	close(store.release)
	got := <-done
	if got.snap.Result != nil {
		t.Logf("real SDK peer returned status=%s result=%s err=%v", got.snap.Run.Status, got.snap.Result.Content, got.err)
	}
	if got.snap.Run.Status != kernel.RunStatusRunning || !errors.Is(got.err, ErrOutcomeUnknown) || got.snap.Result != nil {
		t.Fatal("actual SDK stdio prompt still completed after earlier session/cancel RPC returned")
	}
}

func TestPR151ACPEnvironmentRequiresExplicitGrant(t *testing.T) {
	t.Setenv("GAOGE_PR151_SYNTHETIC_SECRET", "host-only-audit-marker")
	binary := filepath.Join(t.TempDir(), "sdkagent")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build test peer: %v %s", err, output)
	}
	cwd := t.TempDir()
	for _, tc := range []struct {
		name       string
		env        []string
		shouldOpen bool
	}{
		{name: "explicit_nonempty_allowlist", env: []string{"GAOGE_AUDIT_SAFE=1"}, shouldOpen: false},
		{name: "explicit_empty_allowlist", env: []string{}, shouldOpen: false},
		{name: "nil_allowlist", env: nil, shouldOpen: false},
		{name: "explicit_grant", env: []string{"GAOGE_PR151_SYNTHETIC_SECRET=host-only-audit-marker"}, shouldOpen: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{
				Executable: binary, Args: []string{"--require-audit-marker"}, Env: tc.env,
			}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			id, err := client.OpenSession(ctx, "environment-audit", cwd)
			if id != "" {
				defer client.AbandonRun("environment-audit")
			}
			t.Logf("requested_env_count=%d stored_env_is_nil=%t opened=%t err=%v", len(tc.env), client.config.Command.Env == nil, id != "", err)
			if (id != "" && err == nil) != tc.shouldOpen {
				t.Fatal("ACP subprocess inherited synthetic host-only credential despite explicit environment allowlist")
			}
		})
	}
}

func TestPR151ACPStdioCancelRevokesUnsentPrompt(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "sdkagent")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent").CombinedOutput(); err != nil {
		t.Fatalf("build ACP SDK peer: %v %s", err, output)
	}
	cwd := t.TempDir()
	client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	id, err := client.OpenSession(ctx, "cancel-before-wire", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer client.AbandonRun("cancel-before-wire")
	if err := client.CancelSession(ctx, "cancel-before-wire", id); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err = client.Prompt(ctx, "cancel-before-wire", id, "must not run", func(string) error { calls++; return nil })
	if !errors.Is(err, ErrOutcomeUnknown) || calls != 0 {
		t.Fatalf("revoked Prompt returned %v, chunks=%d", err, calls)
	}
}
