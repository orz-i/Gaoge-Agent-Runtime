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
		return wire.NewSessionResponse{}, ErrInvalidBinding
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
	var streamErr error
	resp, err := h.backend.Prompt(ctx, req, func(update wire.SessionUpdate) error {
		h.mu.Lock()
		conn := h.connection
		h.mu.Unlock()
		if conn == nil {
			streamErr = ErrInvalidBinding
			return streamErr
		}
		streamErr = conn.SessionUpdate(ctx, wire.SessionNotification{SessionId: req.SessionId, Update: update})
		return streamErr
	})
	if streamErr != nil {
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
	return loader.LoadSession(ctx, req, func(update wire.SessionUpdate) error {
		h.mu.Lock()
		conn := h.connection
		h.mu.Unlock()
		if conn == nil {
			return ErrInvalidBinding
		}
		return conn.SessionUpdate(ctx, wire.SessionNotification{SessionId: req.SessionId, Update: update})
	})
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

var _ wire.Agent = (*Host)(nil)
var _ wire.AgentLoader = (*Host)(nil)
var _ wire.AgentSessionResumer = (*Host)(nil)
