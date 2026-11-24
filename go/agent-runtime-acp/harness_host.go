package acp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	wire "github.com/caelis-labs/acp-go-sdk"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

// HarnessSessions are Host callbacks selecting the original authenticated
// SessionClient and allocating durable Turn IDs. ACP never selects a Model,
// approves a Tool or builds an independent Agent loop.
type HarnessSessions struct {
	New         func(context.Context, string) (string, error)
	Resolve     func(context.Context, string) (*harness.SessionClient, error)
	BeginTurn   func(context.Context, string) (string, error)
	CurrentTurn func(context.Context, string) (string, error)
}
type HarnessHostedAgent struct{ Sessions HarnessSessions }

func (h HarnessHostedAgent) NewSession(ctx context.Context, req wire.NewSessionRequest) (wire.NewSessionResponse, error) {
	if h.Sessions.New == nil || h.Sessions.Resolve == nil {
		return wire.NewSessionResponse{}, ErrInvalidBinding
	}
	id, err := h.Sessions.New(ctx, req.Cwd)
	if err != nil || id == "" {
		return wire.NewSessionResponse{}, errors.Join(ErrInvalidBinding, err)
	}
	client, err := h.Sessions.Resolve(ctx, id)
	if err != nil || client == nil {
		return wire.NewSessionResponse{}, errors.Join(ErrInvalidBinding, err)
	}
	return wire.NewSessionResponse{SessionId: wire.SessionId(id)}, nil
}
func (h HarnessHostedAgent) Prompt(ctx context.Context, req wire.PromptRequest, emit func(wire.SessionUpdate) error) (wire.PromptResponse, error) {
	if h.Sessions.Resolve == nil || h.Sessions.BeginTurn == nil {
		return wire.PromptResponse{}, ErrInvalidBinding
	}
	var prompt strings.Builder
	for _, part := range req.Prompt {
		if part.Text != nil {
			prompt.WriteString(part.Text.Text)
		} else if part.ResourceLink != nil {
			// A URI is untrusted prompt content, not automatic filesystem/network
			// authority. Local Tool grants remain sealed by the Host.
			prompt.WriteString("\nResource: ")
			prompt.WriteString(part.ResourceLink.Uri)
		} else {
			return wire.PromptResponse{}, ErrInvalidBinding
		}
	}
	if strings.TrimSpace(prompt.String()) == "" {
		return wire.PromptResponse{}, ErrInvalidBinding
	}
	session, err := h.Sessions.Resolve(ctx, string(req.SessionId))
	if err != nil || session == nil {
		return wire.PromptResponse{}, errors.Join(ErrInvalidBinding, err)
	}
	turnID, err := h.Sessions.BeginTurn(ctx, string(req.SessionId))
	if err != nil || turnID == "" {
		return wire.PromptResponse{}, errors.Join(ErrInvalidBinding, err)
	}
	// Execution and restoration belong exclusively to the existing Harness.
	snapshot, err := session.Prompt(ctx, turnID, prompt.String())
	if err != nil {
		return wire.PromptResponse{}, err
	}
	switch snapshot.Turn.Status {
	case harness.TurnCompleted:
		if snapshot.Output == nil {
			return wire.PromptResponse{}, ErrOutcomeUnknown
		}
		answer := nativeOutput(snapshot.Output.Content)
		if err = emit(wire.UpdateAgentMessageText(answer)); err != nil {
			return wire.PromptResponse{}, err
		}
		return wire.PromptResponse{StopReason: wire.StopReasonEndTurn}, nil
	case harness.TurnCancelled:
		return wire.PromptResponse{StopReason: wire.StopReasonCancelled}, nil
	case harness.TurnFailed:
		return wire.PromptResponse{StopReason: wire.StopReasonRefusal}, nil
	case harness.TurnAccepted, harness.TurnRunning, harness.TurnWaitingInput, harness.TurnPausedBudget:
		return wire.PromptResponse{}, ErrOutcomeUnknown
	default:
		// Waiting for Host approval, budget or an unresolved external result
		// cannot be mapped to a falsely successful ACP prompt completion.
		return wire.PromptResponse{}, ErrOutcomeUnknown
	}
}
func (h HarnessHostedAgent) Cancel(ctx context.Context, n wire.CancelNotification) error {
	if h.Sessions.Resolve == nil || h.Sessions.CurrentTurn == nil {
		return ErrInvalidBinding
	}
	id := string(n.SessionId)
	session, err := h.Sessions.Resolve(ctx, id)
	if err != nil || session == nil {
		return errors.Join(ErrInvalidBinding, err)
	}
	turnID, err := h.Sessions.CurrentTurn(ctx, id)
	if err != nil || turnID == "" {
		return errors.Join(ErrInvalidBinding, err)
	}
	_, err = session.Cancel(ctx, turnID, "ACP Client requested cancellation")
	return err
}
func nativeOutput(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var result agent.Result
	if json.Unmarshal(raw, &result) == nil && result.Content != "" {
		return result.Content
	}
	return string(raw)
}

var _ HostedAgent = HarnessHostedAgent{}
