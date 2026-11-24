package harnesssqlite_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
)

func waitingInteraction(t *testing.T, host *harnesssqlite.Store) harness.Interaction {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	turn := harness.Turn{ID: "turn", SessionID: "session", HostTurn: request().HostTurn, ConfigSnapshotID: "config", Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := host.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	inv := harness.Invocation{ID: "invocation", TurnID: turn.ID, ExecutionRefID: "run", Status: harness.InvocationRunning, Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := host.CreateInvocation(ctx, inv); err != nil {
		t.Fatal(err)
	}
	i := harness.Interaction{ID: "approval", TurnID: turn.ID, InvocationID: inv.ID, Key: "approve", Kind: harness.InteractionChoice, Schema: json.RawMessage(`{"type":"object"}`), Status: harness.InteractionWaiting, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := host.CreateInteraction(ctx, i, turn.Revision, inv.Revision); err != nil {
		t.Fatal(err)
	}
	inv.Status = harness.InvocationWaitingInput
	if _, err := host.UpdateInvocation(ctx, inv, inv.Revision); err != nil {
		t.Fatal(err)
	}
	turn.Status = harness.TurnWaitingInput
	if _, err := host.UpdateTurn(ctx, turn, turn.Revision); err != nil {
		t.Fatal(err)
	}
	return i
}

func resolve(t *testing.T, host *harnesssqlite.Store, item harness.Interaction, answer string) bool {
	t.Helper()
	item.Status = harness.InteractionResolved
	item.Response = json.RawMessage(answer)
	result, err := host.ResolveInteraction(t.Context(), item, item.Revision)
	if errors.Is(err, harness.ErrConflict) {
		return false
	}
	if err != nil || result.Interaction.Revision != 2 {
		t.Fatalf("resolve rev 1: %+v %v", result, err)
	}
	return true
}

func TestInteractionWriterWorker(t *testing.T) {
	path := os.Getenv(workerProfile)
	if path == "" {
		t.Skip("subprocess worker")
	}
	host := reopenHost(t, path)
	i, err := host.GetInteraction(t.Context(), "approval")
	if err != nil || i.Revision != 1 {
		t.Fatalf("initial interaction: %+v %v", i, err)
	}
	fmt.Println("ready")
	if _, err = bufio.NewReader(os.Stdin).ReadByte(); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("won=%t\n", resolve(t, host, i, `{"answer":"worker"}`))
}

func TestInteractionResolutionCASAcrossProcesses(t *testing.T) {
	path, host := newHost(t)
	item := waitingInteraction(t, host)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInteractionWriterWorker$")
	cmd.Env = append(os.Environ(), workerProfile+"="+path)
	cmd.Stderr = os.Stderr
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	lines := bufio.NewScanner(output)
	if !lines.Scan() || lines.Text() != "ready" {
		t.Fatalf("worker not ready: %q %v", lines.Text(), lines.Err())
	}
	if _, err = input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	parentWon := resolve(t, host, item, `{"answer":"parent"}`)
	if !lines.Scan() || lines.Text() != fmt.Sprintf("won=%t", !parentWon) {
		t.Fatalf("parent won %t, worker: %q", parentWon, lines.Text())
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err = host.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := reopenHost(t, path)
	done, err := reopened.GetInteraction(t.Context(), item.ID)
	want := `{"answer":"worker"}`
	if parentWon {
		want = `{"answer":"parent"}`
	}
	if err != nil || done.Status != harness.InteractionResolved || done.Revision != 2 || string(done.Response) != want {
		t.Fatalf("durable decision: %+v %v", done, err)
	}
	turn, err := reopened.GetTurn(t.Context(), item.TurnID)
	if err != nil || turn.Status != harness.TurnWaitingInput || turn.Revision != 2 {
		t.Fatalf("unexpected owner transition: %+v %v", turn, err)
	}
	inv, err := reopened.GetInvocation(t.Context(), item.InvocationID)
	if err != nil || inv.Status != harness.InvocationWaitingInput || inv.Revision != 2 {
		t.Fatalf("unexpected invocation transition: %+v %v", inv, err)
	}
}
