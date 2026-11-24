package acp

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	wire "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// The physical peer is a genuine ACP SDK subprocess, not an in-process fake.
// Neither stream delivery nor a pending Host permission decision may own the
// cancellation mutex or make process teardown depend on a Host callback.
func TestACPStdioHostCallbacksCannotBlockCancelOrTeardown(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "callback-agent")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./testdata/sdkagent")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build ACP SDK agent: %v %s", err, output)
	}
	t.Run("blocked-stream", func(t *testing.T) {
		cwd := t.TempDir()
		client, err := NewStdioClient(StdioConfig{Cwd: cwd, Command: stdio.Command{Executable: binary}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
		defer cancel()
		sid, err := client.OpenSession(ctx, "stream-run", cwd)
		if err != nil {
			t.Fatal(err)
		}
		defer client.AbandonRun("stream-run")
		callbackRelease := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(callbackRelease) }) }
		defer release()
		entered := make(chan struct{}, 1)
		promptResult := make(chan error, 1)
		go func() {
			_, e := client.Prompt(ctx, "stream-run", sid, "hello", func(string) error {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-callbackRelease
				return nil
			})
			promptResult <- e
		}()
		select {
		case <-entered:
		case e := <-promptResult:
			t.Fatalf("Prompt returned before stream delivery: %v", e)
		case <-time.After(5 * time.Second):
			t.Fatal("SDK Agent did not deliver streamed text")
		}
		// Stream delivery is still in progress. The SDK response barrier
		// must not let the original Prompt be signed before it completes.
		select {
		case e := <-promptResult:
			t.Fatalf("Prompt returned before stream callback: %v", e)
		default:
		}
		cancelCtx, stopCancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer stopCancel()
		cancelResult := make(chan error, 1)
		go func() { cancelResult <- client.CancelSession(cancelCtx, "stream-run", sid) }()
		cancelReturned := false
		select {
		case e := <-cancelResult:
			cancelReturned = true
			if e != nil && !errors.Is(e, context.DeadlineExceeded) {
				t.Errorf("unexpected cancellation result: %v", e)
			}
		case <-time.After(time.Second):
			t.Error("CancelSession blocked behind Host stream callback past its deadline")
		}
		closed := make(chan struct{})
		go func() { client.AbandonRun("stream-run"); close(closed) }()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Error("AbandonRun blocked waiting for Host stream callback")
		}
		// Release after checking cancellation and teardown. A late callback
		// cannot turn a terminated original Prompt into a signed success.
		release()
		if !cancelReturned {
			select {
			case <-cancelResult:
			case <-time.After(4 * time.Second):
				t.Fatal("CancelSession never returned")
			}
		}
		select {
		case <-closed:
		case <-time.After(4 * time.Second):
			t.Fatal("AbandonRun never returned")
		}
		select {
		case e := <-promptResult:
			if e == nil {
				t.Fatal("terminated Prompt was incorrectly treated as completed")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("Prompt did not exit after subprocess teardown")
		}
	})
	t.Run("blocked-permission", func(t *testing.T) {
		cwd := t.TempDir()
		entered := make(chan struct{}, 1)
		releasePermission := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releasePermission) }) }
		defer release()
		client, err := NewStdioClient(StdioConfig{
			Cwd: cwd, Command: stdio.Command{Executable: binary},
			Permission: func(_ context.Context, _ string, _ wire.RequestPermissionRequest) (wire.PermissionOptionId, bool) {
				select {
				case entered <- struct{}{}:
				default:
				}
				<-releasePermission
				return "allow-once", true
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
		defer cancel()
		sid, err := client.OpenSession(ctx, "permission-run", cwd)
		if err != nil {
			t.Fatal(err)
		}
		defer client.AbandonRun("permission-run")
		promptResult := make(chan error, 1)
		go func() {
			_, e := client.Prompt(ctx, "permission-run", sid, "request-permission", nil)
			promptResult <- e
		}()
		select {
		case <-entered:
		case e := <-promptResult:
			t.Fatalf("Prompt returned without Host permission: %v", e)
		case <-time.After(5 * time.Second):
			t.Fatal("SDK Agent did not request permission")
		}
		cancelCtx, stopCancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer stopCancel()
		cancelResult := make(chan error, 1)
		go func() { cancelResult <- client.CancelSession(cancelCtx, "permission-run", sid) }()
		cancelReturned := false
		select {
		case <-cancelResult:
			cancelReturned = true
		case <-time.After(time.Second):
			t.Error("CancelSession blocked behind Host permission callback")
		}
		closed := make(chan struct{})
		go func() { client.AbandonRun("permission-run"); close(closed) }()
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Error("AbandonRun waited for an uncooperative permission callback")
		}
		release()
		if !cancelReturned {
			select {
			case <-cancelResult:
			case <-time.After(4 * time.Second):
				t.Fatal("CancelSession never returned after permission release")
			}
		}
		select {
		case <-closed:
		case <-time.After(4 * time.Second):
			t.Fatal("AbandonRun never returned")
		}
		select {
		case e := <-promptResult:
			if e == nil {
				t.Fatal("late permission decision incorrectly completed a terminated Prompt")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("Prompt did not return after revoked permission and process teardown")
		}
	})
}
