package acp

import (
	"context"
	"errors"
	"strings"
	"testing"

	wire "github.com/caelis-labs/acp-go-sdk"
)

const secretBackendError = "internal Provider Bearer secret-value"

type leakingHostedBackend struct{}

func (leakingHostedBackend) NewSession(context.Context, wire.NewSessionRequest) (wire.NewSessionResponse, error) {
	return wire.NewSessionResponse{SessionId: "private-session-id"}, errors.New(secretBackendError)
}
func (leakingHostedBackend) Prompt(context.Context, wire.PromptRequest, func(wire.SessionUpdate) error) (wire.PromptResponse, error) {
	return wire.PromptResponse{StopReason: wire.StopReasonEndTurn}, errors.New(secretBackendError)
}
func (leakingHostedBackend) Cancel(context.Context, wire.CancelNotification) error {
	return errors.New(secretBackendError)
}
func (leakingHostedBackend) LoadSession(context.Context, wire.LoadSessionRequest, func(wire.SessionUpdate) error) (wire.LoadSessionResponse, error) {
	return wire.LoadSessionResponse{}, errors.New(secretBackendError)
}
func (leakingHostedBackend) ResumeSession(context.Context, wire.ResumeSessionRequest) (wire.ResumeSessionResponse, error) {
	return wire.ResumeSessionResponse{}, errors.New(secretBackendError)
}

func TestACPHostRedactsPostDispatchProviderAndExecutionErrors(t *testing.T) {
	host, err := NewHost(HostDependencies{Backend: leakingHostedBackend{},
		Authorize: func(context.Context, string, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	check := func(op string, err error) {
		t.Helper()
		if !errors.Is(err, ErrOutcomeUnknown) || strings.Contains(err.Error(), secretBackendError) {
			t.Fatalf("%s: leaked secret or claimed no effect: %v", op, err)
		}
	}
	session, err := host.NewSession(t.Context(), wire.NewSessionRequest{Cwd: t.TempDir(), McpServers: []wire.McpServer{}})
	check("session/new", err)
	if session.SessionId != "" {
		t.Fatalf("exposed unverified session identity: %+v", session)
	}
	result, err := host.Prompt(t.Context(), wire.PromptRequest{SessionId: "host",
		Prompt: []wire.ContentBlock{wire.TextBlock("goal")}})
	check("session/prompt", err)
	if result.StopReason != "" {
		t.Fatalf("projected success despite provider error: %+v", result)
	}
	check("session/cancel", host.Cancel(t.Context(), wire.CancelNotification{SessionId: "host"}))
	_, err = host.LoadSession(t.Context(), wire.LoadSessionRequest{SessionId: "host", Cwd: t.TempDir()})
	check("session/load", err)
	_, err = host.ResumeSession(t.Context(), wire.ResumeSessionRequest{SessionId: "host", Cwd: t.TempDir()})
	check("session/resume", err)
}
