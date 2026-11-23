package acp

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"

	wire "github.com/caelis-labs/acp-go-sdk"
)

// HostedAgent delegates all execution to the Host's existing Agent/Harness
// runner. ACP transports selected sessions; it does not execute a Tool/model.
type HostedAgent interface {
	NewSession(context.Context, wire.NewSessionRequest) (wire.NewSessionResponse, error)
	Prompt(context.Context, wire.PromptRequest, func(wire.SessionUpdate) error) (wire.PromptResponse, error)
	Cancel(context.Context, wire.CancelNotification) error
}
type HostedLoader interface {
	LoadSession(context.Context, wire.LoadSessionRequest, func(wire.SessionUpdate) error) (wire.LoadSessionResponse, error)
}
type HostedResumer interface {
	ResumeSession(context.Context, wire.ResumeSessionRequest) (wire.ResumeSessionResponse, error)
}

// Authorize authenticates Client and original session for EACH method.
// This never replaces Host per-Tool approvals in the Runner.
type Authorize func(context.Context, string, string) error
type HostDependencies struct {
	Backend   HostedAgent
	Authorize Authorize
}
type Host struct {
	backend    HostedAgent
	authorize  Authorize
	mu         sync.Mutex
	connection *wire.AgentSideConnection
}

func NewHost(deps HostDependencies) (*Host, error) {
	if deps.Backend == nil || deps.Authorize == nil {
		return nil, ErrInvalidBinding
	}
	return &Host{backend: deps.Backend, authorize: deps.Authorize}, nil
}
func (h *Host) NewConnection(peerInput io.Writer, peerOutput io.Reader) (*wire.AgentSideConnection, error) {
	if h == nil || peerInput == nil || peerOutput == nil {
		return nil, ErrInvalidBinding
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.connection != nil {
		return nil, ErrInvalidBinding
	}
	h.connection = wire.NewAgentSideConnection(h, peerInput, peerOutput)
	return h.connection, nil
}
func (h *Host) Initialize(ctx context.Context, req wire.InitializeRequest) (wire.InitializeResponse, error) {
	if h == nil || req.ProtocolVersion != wire.ProtocolVersion(wire.WireProtocolVersion) ||
		h.authorize(ctx, "initialize", "") != nil {
		return wire.InitializeResponse{}, ErrInvalidBinding
	}
	capabilities := wire.AgentCapabilities{}
	if _, ok := h.backend.(HostedLoader); ok {
		capabilities.LoadSession = true
	}
	if _, ok := h.backend.(HostedResumer); ok {
		capabilities.SessionCapabilities.Resume = &wire.SessionResumeCapabilities{}
	}
	return wire.InitializeResponse{
		ProtocolVersion:   wire.ProtocolVersion(wire.WireProtocolVersion),
		AgentInfo:         &wire.Implementation{Name: "gaoge-agent-runtime", Version: "1.0.0"},
		AgentCapabilities: capabilities, AuthMethods: []wire.AuthMethod{},
	}, nil
}
func (h *Host) NewSession(ctx context.Context, req wire.NewSessionRequest) (wire.NewSessionResponse, error) {
	if h == nil || h.authorize(ctx, "session/new", "") != nil ||
		!filepath.IsAbs(req.Cwd) || filepath.Clean(req.Cwd) != req.Cwd ||
		len(req.McpServers) != 0 || len(req.AdditionalDirectories) != 0 || req.SystemPrompt != nil {
		return wire.NewSessionResponse{}, ErrInvalidBinding
	}
	resp, err := h.backend.NewSession(ctx, req)
	if err != nil || strings.TrimSpace(string(resp.SessionId)) == "" {
		return wire.NewSessionResponse{}, errors.Join(ErrInvalidBinding, err)
	}
	if h.authorize(ctx, "session/created", string(resp.SessionId)) != nil {
		// The Host's NewSession has already produced an effect. A late
		// identity/revocation failure is not a no-effect authorization
		// refusal: callers must reconcile the original session, not
		// assume retrying session/new is safe.
		return wire.NewSessionResponse{}, ErrOutcomeUnknown
	}
	return resp, nil
}
func (h *Host) Prompt(ctx context.Context, req wire.PromptRequest) (wire.PromptResponse, error) {
	if h == nil || strings.TrimSpace(string(req.SessionId)) == "" || len(req.Prompt) == 0 ||
		h.authorize(ctx, "session/prompt", string(req.SessionId)) != nil {
		return wire.PromptResponse{}, ErrInvalidBinding
	}
	for _, block := range req.Prompt {
		if block.Text == nil && block.ResourceLink == nil {
			return wire.PromptResponse{}, ErrInvalidBinding
		}
	}
	stream := h.newStream(ctx, req.SessionId)
	resp, err := h.backend.Prompt(ctx, req, stream.send)
	if streamErr := stream.finish(); streamErr != nil {
		return wire.PromptResponse{}, streamErr
	}
	if err != nil {
		return wire.PromptResponse{}, err
	}
	switch resp.StopReason {
	case wire.StopReasonEndTurn, wire.StopReasonCancelled, wire.StopReasonMaxTokens,
		wire.StopReasonMaxTurnRequests, wire.StopReasonRefusal:
		return resp, nil
	default:
		return wire.PromptResponse{}, ErrOutcomeUnknown
	}
}
func (h *Host) Cancel(ctx context.Context, n wire.CancelNotification) error {
	if h == nil || strings.TrimSpace(string(n.SessionId)) == "" ||
		h.authorize(ctx, "session/cancel", string(n.SessionId)) != nil {
		return ErrInvalidBinding
	}
	return h.backend.Cancel(ctx, n)
}
func (h *Host) LoadSession(ctx context.Context, req wire.LoadSessionRequest) (wire.LoadSessionResponse, error) {
	loader, ok := h.backend.(HostedLoader)
	if !ok || h.authorize(ctx, "session/load", string(req.SessionId)) != nil ||
		!filepath.IsAbs(req.Cwd) || len(req.McpServers) != 0 ||
		len(req.AdditionalDirectories) != 0 || req.SystemPrompt != nil {
		return wire.LoadSessionResponse{}, ErrInvalidBinding
	}
	stream := h.newStream(ctx, req.SessionId)
	response, err := loader.LoadSession(ctx, req, stream.send)
	if streamErr := stream.finish(); streamErr != nil {
		return wire.LoadSessionResponse{}, streamErr
	}
	return response, err
}
func (h *Host) ResumeSession(ctx context.Context, req wire.ResumeSessionRequest) (wire.ResumeSessionResponse, error) {
	resumer, ok := h.backend.(HostedResumer)
	if !ok || h.authorize(ctx, "session/resume", string(req.SessionId)) != nil ||
		!filepath.IsAbs(req.Cwd) || len(req.McpServers) != 0 ||
		len(req.AdditionalDirectories) != 0 || req.SystemPrompt != nil {
		return wire.ResumeSessionResponse{}, ErrInvalidBinding
	}
	return resumer.ResumeSession(ctx, req)
}

// sessionStream is a scoped physical notification lease. The Host backend
// may call emit concurrently, but updates are sent in original call order
// and must not escape after Prompt or LoadSession has returned. A late
// callback can never append to an already acknowledged ACP turn.
type sessionStream struct {
	mu         sync.Mutex
	ctx        context.Context
	session    wire.SessionId
	connection *wire.AgentSideConnection
	closed     bool
	firstErr   error
}

func (h *Host) newStream(ctx context.Context, session wire.SessionId) *sessionStream {
	h.mu.Lock()
	connection := h.connection
	h.mu.Unlock()
	return &sessionStream{ctx: ctx, session: session, connection: connection}
}

func (s *sessionStream) send(update wire.SessionUpdate) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrOutcomeUnknown
	}
	if s.firstErr != nil {
		return s.firstErr
	}
	if s.connection == nil {
		s.firstErr = ErrInvalidBinding
	} else {
		s.firstErr = s.connection.SessionUpdate(s.ctx, wire.SessionNotification{
			SessionId: s.session, Update: update,
		})
	}
	return s.firstErr
}

func (s *sessionStream) finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.firstErr
}

var _ wire.Agent = (*Host)(nil)
var _ wire.AgentLoader = (*Host)(nil)
var _ wire.AgentSessionResumer = (*Host)(nil)
