package acp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestACPStdioSDKV1ExternalAgentAndSessionBinding(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "sdkagent")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent")
	build.Env = os.Environ()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ACP sdk peer: %v %s", err, output)
	}
	cwd := t.TempDir()
	client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, Binding: Binding{
		TargetID: "stdio-agent", Revision: "sdk-v1", Cwd: cwd, Client: client,
	}, AttestationKey: []byte("01234567890123456789012345678901")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req := startRequest("stdio-run")
	req.Goal = "hello"
	snap, err := runner.StartRun(ctx, req)
	if err != nil || snap.Run.Status != "completed" {
		t.Fatalf("SDK ACP prompt not completed %+v %v", snap.Run, err)
	}
	var output struct{ Text string }
	if json.Unmarshal(snap.Result.Content, &output) != nil || output.Text != "SDK response" {
		t.Fatalf("SDK stream mismatch %s", snap.Result.Content)
	}
	if _, err = runner.LoadRun(t.Context(), snap.Run.ID); err != nil {
		t.Fatalf("restart receipt lost: %v", err)
	}
	// A forged notification must never be aggregated into a successful result.
	req = startRequest("stdio-forged")
	req.Goal = "wrong-session"
	pending, err := runner.StartRun(ctx, req)
	if !errors.Is(err, ErrOutcomeUnknown) || pending.Run.Status != "running" {
		t.Fatalf("foreign ACP Session was accepted: %+v %v", pending.Run, err)
	}
	if _, err = runner.LoadRun(ctx, pending.Run.ID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("foreign Session settled: %v", err)
	}
}
func TestACPStdioPermissionDenyAndSelection(t *testing.T) {
	calls := 0
	run := &stdioRun{sessionID: "bound"}
	cb := &stdioCallbacks{runID: "run", run: run}
	query := wire.RequestPermissionRequest{SessionId: "bound", Options: []wire.PermissionOption{
		{OptionId: "allow-once", Name: "Allow once", Kind: "allow_once"},
	}}
	denied, err := cb.RequestPermission(t.Context(), query)
	if err != nil || denied.Outcome.Cancelled == nil {
		t.Fatalf("default permission was not denied %+v %v", denied, err)
	}
	cb.decision = func(context.Context, string, wire.RequestPermissionRequest) (wire.PermissionOptionId, bool) {
		calls++
		return "allow-once", true
	}
	selected, err := cb.RequestPermission(t.Context(), query)
	if err != nil || selected.Outcome.Selected == nil || selected.Outcome.Selected.OptionId != "allow-once" {
		t.Fatalf("Host permitted choice ignored: %+v %v", selected, err)
	}
	query.SessionId = "foreign"
	if _, err = cb.RequestPermission(t.Context(), query); !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("foreign permission request accepted %v", err)
	}
	if calls != 1 {
		t.Fatalf("unauthorized Host callback invoked %d", calls)
	}
}
