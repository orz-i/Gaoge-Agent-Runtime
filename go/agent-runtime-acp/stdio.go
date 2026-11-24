package acp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wire "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

// PermissionDecision is the Host-owned per-call permission decision.
// A nil callback denies all requests; the returned option must be one the
// remote Agent actually offered for the SAME frozen ACP Session.
type PermissionDecision func(context.Context, string, wire.RequestPermissionRequest) (wire.PermissionOptionId, bool)

// StdioConfig is Host configuration, never an Agent-generated or persisted
// capability. ACP v1 FS, terminal and extra roots are NOT advertised.
type StdioConfig struct {
	Command    stdio.Command
	Cwd        string
	Permission PermissionDecision
}
type StdioClient struct {
	config StdioConfig
	mu     sync.Mutex
	runs   map[string]*stdioRun
}
type stdioRun struct {
	mu               sync.Mutex
	sendGate         chan struct{} // Context-aware exclusive physical frame sender.
	frameReady       chan struct{} // A peer callback waits for the original Prompt frame to finish sending.
	process          *stdio.ClientProcess
	sessionID        string
	onChunk          func(string) error
	promptActive     bool
	cancelRequested  bool
	promptDispatched bool
	closed           bool
	invalid          bool
}

func NewStdioClient(config StdioConfig) (*StdioClient, error) {
	if strings.TrimSpace(config.Command.Executable) == "" ||
		!filepath.IsAbs(config.Cwd) || filepath.Clean(config.Cwd) != config.Cwd {
		return nil, ErrInvalidBinding
	}
	// The executable, working directory and environment are explicit Host
	// grants. Neither nil nor an empty allowlist inherits ambient credentials.
	// The upstream SDK collapses a zero-length Env into nil a second time;
	// a fixed non-secret marker keeps the child environment explicit.
	if config.Command.Dir != "" && config.Command.Dir != config.Cwd {
		return nil, ErrInvalidBinding
	}
	config.Command.Dir = config.Cwd
	config.Command.Args = append([]string(nil), config.Command.Args...)
	config.Command.Env = append([]string(nil), config.Command.Env...)
	if len(config.Command.Env) == 0 {
		config.Command.Env = []string{"GAOGE_ACP_ENV_ISOLATED=1"}
	}
	return &StdioClient{config: config, runs: make(map[string]*stdioRun)}, nil
}

type stdioCallbacks struct {
	runID    string
	run      *stdioRun
	decision PermissionDecision
}

func (cb *stdioCallbacks) SessionUpdate(ctx context.Context, n wire.SessionNotification) error {
	if err := cb.run.awaitOriginalFrame(ctx); err != nil {
		return ErrOutcomeUnknown
	}
	cb.run.mu.Lock()
	if cb.run.closed || cb.run.sessionID == "" || string(n.SessionId) != cb.run.sessionID {
		cb.run.invalid = true
		cb.run.mu.Unlock()
		return ErrSessionMismatch
	}
	if n.Update.AgentMessageChunk == nil || n.Update.AgentMessageChunk.Content.Text == nil {
		cb.run.mu.Unlock()
		return nil
	}
	if !cb.run.promptActive || cb.run.onChunk == nil {
		// No original Prompt owns this agent output. Refuse unsolicited
		// chunks rather than letting another logical turn inherit them.
		cb.run.invalid = true
		cb.run.mu.Unlock()
		return ErrOutcomeUnknown
	}
	// The SDK serializes notifications and Wait does not return a successful
	// Prompt response until all preceding notifications have been handled.
	// Capture the original stream callback under the session lock, but invoke
	// Host code OUTSIDE it. An unresponsive output consumer must never hold
	// the cancellation, permission-revocation or process-teardown mutex.
	onChunk := cb.run.onChunk
	text := n.Update.AgentMessageChunk.Content.Text.Text
	cb.run.mu.Unlock()
	return onChunk(text)
}
func (cb *stdioCallbacks) RequestPermission(ctx context.Context, q wire.RequestPermissionRequest) (wire.RequestPermissionResponse, error) {
	deny := wire.RequestPermissionResponse{Outcome: wire.NewRequestPermissionOutcomeCancelled()}
	if err := cb.run.awaitOriginalFrame(ctx); err != nil {
		return deny, ErrOutcomeUnknown
	}
	cb.run.mu.Lock()
	same := cb.run.sessionID != "" && cb.run.sessionID == string(q.SessionId)
	if !same {
		cb.run.invalid = true
	}
	admitted := cb.run.promptActive && !cb.run.cancelRequested && !cb.run.invalid
	cb.run.mu.Unlock()
	if !same {
		return deny, ErrSessionMismatch
	}
	// A session/new acknowledgement is not a Host authorization to run
	// tools. Permission requests require an already admitted physical Prompt
	// and are revoked when its cancellation begins or it returns.
	if !admitted || cb.decision == nil {
		return deny, nil
	}
	option, approved := cb.decision(ctx, cb.runID, q)
	if !approved {
		return deny, nil
	}
	cb.run.mu.Lock()
	stillAdmitted := cb.run.promptActive && !cb.run.cancelRequested && !cb.run.invalid && cb.run.sessionID == string(q.SessionId)
	cb.run.mu.Unlock()
	if !stillAdmitted {
		return deny, nil // Host may have cancelled while permission was being decided.
	}
	for _, offered := range q.Options {
		if offered.OptionId == option && option != "" {
			return wire.RequestPermissionResponse{Outcome: wire.NewRequestPermissionOutcomeSelected(option)}, nil
		}
	}
	return deny, ErrInvalidBinding
}

func (c *StdioClient) OpenSession(ctx context.Context, runID, cwd string) (string, error) {
	if c == nil || strings.TrimSpace(runID) == "" || cwd != c.config.Cwd {
		return "", ErrInvalidBinding
	}
	run := &stdioRun{sendGate: make(chan struct{}, 1)}
	run.sendGate <- struct{}{}
	c.mu.Lock()
	if _, exists := c.runs[runID]; exists {
		c.mu.Unlock()
		return "", ErrOutcomeUnknown
	}
	c.runs[runID] = run
	c.mu.Unlock()
	cleanup := func() { c.closeRun(runID, run) }
	callbacks := &stdioCallbacks{runID: runID, run: run, decision: c.config.Permission}
	process, err := stdio.StartClient(ctx, callbacks, c.config.Command, wire.ConnectionOptions{})
	if err != nil {
		cleanup()
		return "", err
	}
	run.mu.Lock()
	if run.closed {
		run.mu.Unlock()
		shutdown(process)
		return "", ErrOutcomeUnknown
	}
	run.process = process
	run.mu.Unlock()
	init, err := process.Connection.Initialize(ctx, wire.InitializeRequest{
		ProtocolVersion: wire.ProtocolVersion(wire.WireProtocolVersion),
		ClientInfo:      &wire.Implementation{Name: "gaoge-agent-runtime", Version: "1.0.0"},
	})
	if err != nil || init.ProtocolVersion != wire.ProtocolVersion(wire.WireProtocolVersion) {
		cleanup()
		return "", errors.Join(ErrInvalidBinding, err)
	}
	newSession, err := process.Connection.NewSession(ctx, wire.NewSessionRequest{
		Cwd: cwd, McpServers: []wire.McpServer{},
	})
	if err != nil || string(newSession.SessionId) == "" {
		cleanup()
		return "", errors.Join(ErrSessionMismatch, err)
	}
	run.mu.Lock()
	if run.closed {
		run.mu.Unlock()
		return "", ErrOutcomeUnknown
	}
	run.sessionID = string(newSession.SessionId)
	run.mu.Unlock()
	return string(newSession.SessionId), nil
}

// acquireSend waits only for original frame order, never for the peer's
// response. Cancellation and AbandonRun must not depend on an unresponsive
// writer releasing a process-local mutex.
func (r *stdioRun) acquireSend(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidBinding
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.sendGate:
		if err := ctx.Err(); err != nil {
			r.releaseSend()
			return err
		}
		return nil
	}
}

func (r *stdioRun) releaseSend() { r.sendGate <- struct{}{} }

// awaitOriginalFrame prevents unsolicited peer updates and permission grants
// before the first Prompt frame has been fully submitted. The transport callback
// may race the writer's return, so it waits without holding the control mutex.
func (r *stdioRun) awaitOriginalFrame(ctx context.Context) error {
	r.mu.Lock()
	ready := r.frameReady
	r.mu.Unlock()
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *StdioClient) Prompt(ctx context.Context, runID, sessionID, goal string, onChunk func(string) error) (string, error) {
	run, err := c.requireRun(runID, sessionID)
	if err != nil {
		return "", err
	}
	// A Cancel arriving before this grant must irrevocably deny Prompt.
	// If Prompt already owns the sender, Cancel waits only until its own
	// deadline; it never grants a post-cancel retry.
	if err = run.acquireSend(ctx); err != nil {
		return "", err
	}
	run.mu.Lock()
	if run.closed || run.cancelRequested || run.promptDispatched || run.invalid {
		revoked := run.cancelRequested && !run.promptDispatched && !run.closed
		run.mu.Unlock()
		run.releaseSend()
		if revoked {
			c.closeRun(runID, run)
		}
		return "", ErrOutcomeUnknown
	}
	process := run.process
	request, err := wire.PrepareClientRequest[wire.PromptResponse](process.Connection, wire.AgentMethodSessionPrompt, wire.PromptRequest{
		SessionId: wire.SessionId(sessionID),
		Prompt:    []wire.ContentBlock{wire.TextBlock(goal)},
	})
	if err != nil {
		run.mu.Unlock()
		run.releaseSend()
		c.closeRun(runID, run)
		return "", err
	}
	defer request.Abandon()
	defer c.closeRun(runID, run)
	run.promptDispatched = true
	run.frameReady = make(chan struct{})
	run.mu.Unlock()

	// Never hold the control mutex while waiting for the SDK writer.
	// The frameReady barrier still prevents callbacks from using an unsent
	// Prompt, and sendGate preserves Prompt-before-Cancel physical order.
	err = request.Dispatch(ctx, wire.DispatchOptions{Abort: func(error) error { return process.Connection.Close() }})
	run.mu.Lock()
	if err == nil && !run.closed {
		run.onChunk = onChunk
		run.promptActive = true
	} else if run.closed {
		err = ErrOutcomeUnknown
	}
	close(run.frameReady)
	run.mu.Unlock()
	run.releaseSend()
	if err != nil {
		return "", err
	}
	resp, err := request.Wait(ctx)
	run.mu.Lock()
	run.promptActive = false
	run.onChunk = nil
	invalid := run.invalid || run.closed
	run.mu.Unlock()
	if invalid {
		return "", ErrSessionMismatch
	}
	if err != nil {
		return "", err
	}
	return string(resp.StopReason), nil
}

func (c *StdioClient) CancelSession(ctx context.Context, runID, sessionID string) error {
	run, err := c.requireRun(runID, sessionID)
	if err != nil {
		return err
	}
	// Revoke all future Prompt admissions and permission callbacks *before*
	// waiting for the peer's writer. A blocked partial frame never extends
	// the caller's cancellation deadline or reopens execution authority.
	run.mu.Lock()
	if run.closed || run.cancelRequested {
		run.mu.Unlock()
		return ErrOutcomeUnknown
	}
	run.cancelRequested = true
	run.mu.Unlock()
	if err := run.acquireSend(ctx); err != nil {
		// The original Cancel intent remains unknown in durable Shadow state.
		// No later caller may repeat this physical notification.
		return err
	}
	defer run.releaseSend()
	run.mu.Lock()
	if run.closed {
		run.mu.Unlock()
		return ErrOutcomeUnknown
	}
	process := run.process
	run.mu.Unlock()
	return process.Connection.Cancel(ctx, wire.CancelNotification{SessionId: wire.SessionId(sessionID)})
}
func (c *StdioClient) requireRun(runID, sessionID string) (*stdioRun, error) {
	if c == nil || runID == "" || sessionID == "" {
		return nil, ErrInvalidBinding
	}
	c.mu.Lock()
	run := c.runs[runID]
	c.mu.Unlock()
	if run == nil {
		return nil, ErrOutcomeUnknown
	}
	run.mu.Lock()
	same := run.sessionID == sessionID && !run.invalid && !run.closed && run.process != nil
	run.mu.Unlock()
	if !same {
		return nil, ErrSessionMismatch
	}
	return run, nil
}

// AbandonRun releases a locally owned ACP subprocess when the Kernel denies
// a delayed session/new result. It does not send a new prompt or cancel RPC;
// the original remote effect remains outcome-unknown in durable evidence.
func (c *StdioClient) AbandonRun(id string) {
	if c == nil || id == "" {
		return
	}
	c.mu.Lock()
	run := c.runs[id]
	c.mu.Unlock()
	if run != nil {
		c.closeRun(id, run)
	}
}

func (c *StdioClient) closeRun(id string, run *stdioRun) {
	c.mu.Lock()
	if c.runs[id] == run {
		delete(c.runs, id)
	}
	c.mu.Unlock()
	// A dead peer may hold the writer indefinitely. Closing must revoke the
	// session without joining sendGate, then terminate its owned process.
	// A concurrent send either observes closed or is aborted by shutdown.
	run.mu.Lock()
	if run.closed {
		run.mu.Unlock()
		return
	}
	run.closed = true
	run.cancelRequested = true
	process := run.process
	run.mu.Unlock()
	shutdown(process)
}
func shutdown(process *stdio.ClientProcess) {
	if process == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// ClientProcess.Shutdown uses Connection.Wait(context.WithoutCancel(ctx)):
	// an uncooperative Host stream/permission callback can therefore make
	// its 5-second process deadline meaningless. This client owns a separate
	// subprocess per Run, so after closeRun revokes its session we terminate
	// that process tree directly instead of waiting for an external callback
	// to permit graceful shutdown. The SDK's process waiter still reaps it.
	// A blocked Host callback cannot be forcibly stopped in Go, but its late
	// return cannot revive a closed session or authorize more physical work.
	_ = process.Connection.Close()
	_ = process.Process.Close()
	_ = process.Process.Wait(ctx)
}

var _ RemoteClient = (*StdioClient)(nil)
var _ wire.Client = (*stdioCallbacks)(nil)
