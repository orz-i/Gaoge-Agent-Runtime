package acp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// An ACP peer can stop reading its stdin after session/new. Sending a large
// but valid Prompt can then block on the kernel pipe before the frame completes.
// Protective Cancel and AbandonRun must not wait for that peer to drain it.
func TestACPBlockedWriterCancelAndAbandonHonorDeadlines(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "blocked-agent")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/blockedagent")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build blocked ACP peer: %v: %s", err, output)
	}
	cwd := t.TempDir()
	client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	sessionID, err := client.OpenSession(ctx, "blocked-run", cwd)
	if err != nil {
		t.Fatalf("open blocked peer: %v", err)
	}
	defer client.AbandonRun("blocked-run")

	promptCtx, stopPrompt := context.WithTimeout(t.Context(), 15*time.Second)
	defer stopPrompt()
	promptDone := make(chan error, 1)
	go func() {
		_, promptErr := client.Prompt(promptCtx, "blocked-run", sessionID, strings.Repeat("A", 200_000), nil)
		promptDone <- promptErr
	}()

	// Observe actual frame dispatch admission, not a guessed sleep. A
	// complete frame cannot fit this peer's unconsumed pipe buffer.
	var run *stdioRun
	var frame chan struct{}
	until := time.After(4 * time.Second)
	for {
		client.mu.Lock()
		run = client.runs["blocked-run"]
		client.mu.Unlock()
		if run != nil {
			run.mu.Lock()
			frame = run.frameReady
			run.mu.Unlock()
		}
		if frame != nil && len(run.sendGate) == 0 {
			select {
			case <-frame:
				t.Fatal("blocked peer unexpectedly received the entire Prompt")
			default:
			}
			break
		}
		select {
		case err := <-promptDone:
			t.Fatalf("Prompt finished before blocked write: %v", err)
		case <-until:
			t.Fatal("Prompt never entered the blocked writer")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancelCtx, stopCancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stopCancel()
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- client.CancelSession(cancelCtx, "blocked-run", sessionID) }()
	select {
	case err := <-cancelDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel should preserve an unknown outcome at its deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelSession blocked behind the physical Prompt write")
	}
	run.mu.Lock()
	revoked := run.cancelRequested
	run.mu.Unlock()
	if !revoked {
		t.Fatal("timed-out cancellation left permission and later Prompt grants active")
	}

	abandoned := make(chan struct{})
	go func() { client.AbandonRun("blocked-run"); close(abandoned) }()
	select {
	case <-abandoned:
	case <-time.After(8 * time.Second):
		t.Fatal("AbandonRun blocked behind an unresponsive ACP writer")
	}
	stopPrompt()
	select {
	case err := <-promptDone:
		if err == nil {
			t.Fatal("abandoned partial Prompt was incorrectly acknowledged")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("Prompt did not exit after its owned process was abandoned")
	}
}
