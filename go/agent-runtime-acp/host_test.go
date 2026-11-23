package acp

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	wire "github.com/caelis-labs/acp-go-sdk"
)

type hostedFake struct {
	mu                     sync.Mutex
	news, prompts, cancels int
	cancelSignal           chan struct{}
}

func (h *hostedFake) NewSession(_ context.Context, req wire.NewSessionRequest) (wire.NewSessionResponse, error) {
	h.mu.Lock()
	h.news++
	h.mu.Unlock()
	return wire.NewSessionResponse{SessionId: "host-session"}, nil
}
func (h *hostedFake) Prompt(_ context.Context, req wire.PromptRequest, emit func(wire.SessionUpdate) error) (wire.PromptResponse, error) {
	h.mu.Lock()
	h.prompts++
	h.mu.Unlock()
	if err := emit(wire.UpdateAgentMessageText("from ")); err != nil {
		return wire.PromptResponse{}, err
	}
	if err := emit(wire.UpdateAgentMessageText("native Host")); err != nil {
		return wire.PromptResponse{}, err
	}
	return wire.PromptResponse{StopReason: wire.StopReasonEndTurn}, nil
}
func (h *hostedFake) Cancel(context.Context, wire.CancelNotification) error {
	h.mu.Lock()
	h.cancels++
	if h.cancelSignal != nil {
		close(h.cancelSignal)
		h.cancelSignal = nil
	}
	h.mu.Unlock()
	return nil
}

type acpTestClient struct {
	mu     sync.Mutex
	chunks []string
}

func (c *acpTestClient) RequestPermission(context.Context, wire.RequestPermissionRequest) (wire.RequestPermissionResponse, error) {
	return wire.RequestPermissionResponse{Outcome: wire.NewRequestPermissionOutcomeCancelled()}, nil
}
func (c *acpTestClient) SessionUpdate(_ context.Context, n wire.SessionNotification) error {
	if n.SessionId != "host-session" {
		return ErrSessionMismatch
	}
	chunk := n.Update.AgentMessageChunk
	if chunk != nil && chunk.Content.Text != nil {
		c.mu.Lock()
		c.chunks = append(c.chunks, chunk.Content.Text.Text)
		c.mu.Unlock()
	}
	return nil
}

func TestACPHostV1RPCStreamingPermissionBoundary(t *testing.T) {
	backend := &hostedFake{cancelSignal: make(chan struct{})}
	cancelReceived := backend.cancelSignal
	auth := func(ctx context.Context, method, session string) error {
		if method == "session/prompt" && session == "blocked" {
			return ErrInvalidBinding
		}
		return nil
	}
	host, err := NewHost(HostDependencies{Backend: backend, Authorize: auth})
	if err != nil {
		t.Fatal(err)
	}
	peer, clientConn := net.Pipe()
	defer func() { _ = peer.Close() }()
	defer func() { _ = clientConn.Close() }()
	srv, err := host.NewConnection(peer, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	callback := &acpTestClient{}
	client := wire.NewClientSideConnection(callback, clientConn, clientConn)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	init, err := client.Initialize(ctx, wire.InitializeRequest{ProtocolVersion: wire.ProtocolVersion(wire.WireProtocolVersion)})
	if err != nil || init.AgentCapabilities.LoadSession || init.AgentCapabilities.SessionCapabilities.Resume != nil {
		t.Fatalf("unsupported recovery capability advertised: %+v %v", init.AgentCapabilities, err)
	}
	cwd, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, wire.NewSessionRequest{Cwd: cwd, McpServers: []wire.McpServer{}})
	if err != nil || session.SessionId != "host-session" {
		t.Fatalf("new session %+v %v", session, err)
	}
	response, err := client.Prompt(ctx, wire.PromptRequest{
		SessionId: session.SessionId, Prompt: []wire.ContentBlock{wire.TextBlock("hello")},
	})
	if err != nil || response.StopReason != wire.StopReasonEndTurn {
		t.Fatalf("prompt %+v %v", response, err)
	}
	callback.mu.Lock()
	joined := ""
	for _, s := range callback.chunks {
		joined += s
	}
	callback.mu.Unlock()
	if joined != "from native Host" {
		t.Fatalf("stream: %q", joined)
	}
	if _, err = client.Prompt(ctx, wire.PromptRequest{SessionId: "blocked", Prompt: []wire.ContentBlock{wire.TextBlock("secret")}}); err == nil {
		t.Fatal("Host denied prompt was dispatched")
	}
	backend.mu.Lock()
	count := backend.prompts
	backend.mu.Unlock()
	if count != 1 {
		t.Fatalf("unauthorized execution: %d", count)
	}
	if err = client.Cancel(ctx, wire.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatalf("cancel %v", err)
	}
	select {
	case <-cancelReceived:
	case <-ctx.Done():
		t.Fatal("cancel notification not delivered")
	}
	backend.mu.Lock()
	cancels := backend.cancels
	backend.mu.Unlock()
	if cancels != 1 {
		t.Fatalf("cancel not sent to Host: %d", cancels)
	}
}
func TestACPHostFailsClosedOnUnsupportedMethodsBeforeHost(t *testing.T) {
	backend := &hostedFake{}
	host, err := NewHost(HostDependencies{Backend: backend, Authorize: func(context.Context, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	_, err = host.NewSession(ctx, wire.NewSessionRequest{Cwd: "relative", McpServers: []wire.McpServer{}})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("relative cwd accepted %v", err)
	}
	_, err = host.NewSession(ctx, wire.NewSessionRequest{Cwd: "/workspace", McpServers: []wire.McpServer{{}}})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("unbound MCP accepted %v", err)
	}
	_, err = host.Prompt(ctx, wire.PromptRequest{SessionId: "session", Prompt: []wire.ContentBlock{wire.ImageBlock("", "")}})
	if !errors.Is(err, ErrInvalidBinding) {
		t.Fatalf("unsupported modality accepted %v", err)
	}
	backend.mu.Lock()
	calls := backend.news + backend.prompts
	backend.mu.Unlock()
	if calls != 0 {
		t.Fatal("untrusted request reached Host")
	}
}

// concurrentHostedFake deliberately calls the Host's one Prompt emitter from
// multiple goroutines, then retains it past the terminal response.
type concurrentHostedFake struct {
	hostedFake
	mu   sync.Mutex
	late func(wire.SessionUpdate) error
}

func (h *concurrentHostedFake) Prompt(_ context.Context, _ wire.PromptRequest, emit func(wire.SessionUpdate) error) (wire.PromptResponse, error) {
	h.mu.Lock()
	h.late = emit
	h.mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- emit(wire.UpdateAgentMessageText("x"))
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return wire.PromptResponse{}, err
		}
	}
	return wire.PromptResponse{StopReason: wire.StopReasonEndTurn}, nil
}

func TestACPHostStreamLeaseClosesAfterTerminalAndSerializesConcurrentChunks(t *testing.T) {
	backend := &concurrentHostedFake{}
	host, err := NewHost(HostDependencies{Backend: backend,
		Authorize: func(context.Context, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	peer, clientConn := net.Pipe()
	defer func() { _ = peer.Close() }()
	defer func() { _ = clientConn.Close() }()
	server, err := host.NewConnection(peer, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	callbacks := &acpTestClient{}
	client := wire.NewClientSideConnection(callbacks, clientConn, clientConn)
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err = client.Initialize(ctx, wire.InitializeRequest{ProtocolVersion: wire.ProtocolVersion(wire.WireProtocolVersion)}); err != nil {
		t.Fatal(err)
	}
	session, err := client.NewSession(ctx, wire.NewSessionRequest{Cwd: t.TempDir(), McpServers: []wire.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	ending, err := client.Prompt(ctx, wire.PromptRequest{SessionId: session.SessionId,
		Prompt: []wire.ContentBlock{wire.TextBlock("hello")}})
	if err != nil || ending.StopReason != wire.StopReasonEndTurn {
		t.Fatalf("terminal stream failed %+v %v", ending, err)
	}
	backend.mu.Lock()
	late := backend.late
	backend.mu.Unlock()
	if late == nil || !errors.Is(late(wire.UpdateAgentMessageText("AFTER")), ErrOutcomeUnknown) {
		t.Fatal("an ACP session/update escaped after terminal Prompt return")
	}
	callbacks.mu.Lock()
	defer callbacks.mu.Unlock()
	if len(callbacks.chunks) != 8 {
		t.Fatalf("concurrent chunks lost or late chunk emitted: %#v", callbacks.chunks)
	}
	for _, chunk := range callbacks.chunks {
		if chunk != "x" {
			t.Fatalf("foreign chunk %q", chunk)
		}
	}
}

func TestACPHostPostCreationRevocationCannotClaimNoEffect(t *testing.T) {
	backend := &hostedFake{}
	host, err := NewHost(HostDependencies{Backend: backend,
		Authorize: func(_ context.Context, method, session string) error {
			if method == "session/created" {
				return ErrInvalidBinding
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	created, err := host.NewSession(t.Context(), wire.NewSessionRequest{Cwd: t.TempDir(), McpServers: []wire.McpServer{}})
	if !errors.Is(err, ErrOutcomeUnknown) || created.SessionId != "" {
		t.Fatalf("post-effect refusal masqueraded as no-effect: %+v %v", created, err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.news != 1 {
		t.Fatalf("session was not created: %d", backend.news)
	}
}
