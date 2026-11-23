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
	mu              sync.Mutex
	process         *stdio.ClientProcess
	sessionID       string
	onChunk         func(string) error
	promptActive    bool
	cancelRequested bool
	invalid         bool
}

func NewStdioClient(config StdioConfig) (*StdioClient, error) {
	if strings.TrimSpace(config.Command.Executable) == "" ||
		!filepath.IsAbs(config.Cwd) || filepath.Clean(config.Cwd) != config.Cwd {
		return nil, ErrInvalidBinding
	}
	// The executable and environment are an explicit Host grant, never
	// synthesized from model text or the Agent's advertised name.
	config.Command.Args = append([]string(nil), config.Command.Args...)
	config.Command.Env = append([]string(nil), config.Command.Env...)
	return &StdioClient{config: config, runs: make(map[string]*stdioRun)}, nil
}

type stdioCallbacks struct {
	runID    string
	run      *stdioRun
	decision PermissionDecision
}

func (cb *stdioCallbacks) SessionUpdate(_ context.Context, n wire.SessionNotification) error {
	cb.run.mu.Lock()
	defer cb.run.mu.Unlock()
	if cb.run.sessionID == "" || string(n.SessionId) != cb.run.sessionID {
		cb.run.invalid = true
		return ErrSessionMismatch
	}
	if n.Update.AgentMessageChunk == nil || n.Update.AgentMessageChunk.Content.Text == nil {
		return nil
	}
	if !cb.run.promptActive || cb.run.onChunk == nil {
		// No original Prompt owns this agent output. Refuse unsolicited
		// chunks rather than letting another logical turn inherit them.
		cb.run.invalid = true
		return ErrOutcomeUnknown
	}
	// Serialize original notifications with Prompt return. The original
	// prompt cannot be terminally signed while a stream callback is active.
	return cb.run.onChunk(n.Update.AgentMessageChunk.Content.Text.Text)
}
func (cb *stdioCallbacks) RequestPermission(ctx context.Context, q wire.RequestPermissionRequest) (wire.RequestPermissionResponse, error) {
	deny := wire.RequestPermissionResponse{Outcome: wire.NewRequestPermissionOutcomeCancelled()}
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
	run := &stdioRun{}
	c.mu.Lock()
	if _, exists := c.runs[runID]; exists {
		c.mu.Unlock()
		return "", ErrOutcomeUnknown
	}
	c.runs[runID] = run
	c.mu.Unlock()
	cleanup := func() {
		c.mu.Lock()
		delete(c.runs, runID)
		c.mu.Unlock()
		run.mu.Lock()
		process := run.process
		run.mu.Unlock()
		if process != nil {
			shutdown(process)
		}
	}
	callbacks := &stdioCallbacks{runID: runID, run: run, decision: c.config.Permission}
	process, err := stdio.StartClient(ctx, callbacks, c.config.Command, wire.ConnectionOptions{})
	if err != nil {
		cleanup()
		return "", err
	}
	run.mu.Lock()
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
	run.sessionID = string(newSession.SessionId)
	run.mu.Unlock()
	return run.sessionID, nil
}

func (c *StdioClient) Prompt(ctx context.Context, runID, sessionID, goal string, onChunk func(string) error) (string, error) {
	run, err := c.requireRun(runID, sessionID)
	if err != nil {
		return "", err
	}
	run.mu.Lock()
	run.onChunk = onChunk
	run.promptActive = true
	process := run.process
	run.mu.Unlock()
	defer c.closeRun(runID, run)
	resp, err := process.Connection.Prompt(ctx, wire.PromptRequest{
		SessionId: wire.SessionId(sessionID),
		Prompt:    []wire.ContentBlock{wire.TextBlock(goal)},
	})
	run.mu.Lock()
	run.promptActive = false
	run.onChunk = nil
	invalid := run.invalid
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
	run.mu.Lock()
	run.cancelRequested = true // The original Cancel intent revokes future permission grants.
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
	same := run.sessionID == sessionID && !run.invalid && run.process != nil
	run.mu.Unlock()
	if !same {
		return nil, ErrSessionMismatch
	}
	return run, nil
}
func (c *StdioClient) closeRun(id string, run *stdioRun) {
	c.mu.Lock()
	if c.runs[id] == run {
		delete(c.runs, id)
	}
	c.mu.Unlock()
	shutdown(run.process)
}
func shutdown(process *stdio.ClientProcess) {
	if process == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = process.Shutdown(ctx)
}

var _ RemoteClient = (*StdioClient)(nil)
var _ wire.Client = (*stdioCallbacks)(nil)
