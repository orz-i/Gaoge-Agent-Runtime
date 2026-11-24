package context

import (
	"bytes"
	stdcontext "context"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// The projection never becomes a Model request, Context entry or Artifact.
// It is evaluated afresh for every candidate; failure cannot silently fall
// back to the smaller pre-canonical request.
func (manager *Manager) accountingPayload(ctx stdcontext.Context, original model.Request, window ModelWindow, originalPayload []byte) ([]byte, error) {
	if manager == nil || manager.accounting == nil {
		return originalPayload, nil
	}
	projected, err := manager.accounting(ctx, model.CloneRequest(original), window)
	if err != nil {
		return nil, err
	}
	projected = model.CloneRequest(projected)
	if projected.MaxOutputTokens < 0 || projected.MaxOutputTokens != window.ReservedOutputTokens {
		return nil, ErrInvalidInput
	}
	expected := model.CloneRequest(original)
	expected.ModelOptions = projected.ModelOptions
	expected.MaxOutputTokens = projected.MaxOutputTokens
	if len(projected.Messages) == len(original.Messages)+1 {
		prefix := projected.Messages[0]
		// Only a plain System prompt can be added; the immutable original
		// messages (including media and ProviderReplay) must remain intact.
		if prefix.Role != model.RoleSystem || len(prefix.Content) != 1 || prefix.Content[0].Kind != model.ContentText || model.TextValue(prefix.Content) == "" {
			return nil, ErrInvalidInput
		}
		expected.Messages = append([]model.Message{{Role: model.RoleSystem, Content: model.TextParts(model.TextValue(prefix.Content))}}, expected.Messages...)
	}
	expectedPayload, err := model.MarshalRequestForInputCount(expected)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	payload, err := model.MarshalRequestForInputCount(projected)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	if !bytes.Equal(expectedPayload, payload) {
		return nil, ErrInvalidInput
	}
	return payload, nil
}
