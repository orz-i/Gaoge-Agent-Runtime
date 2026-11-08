package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/internal/providerhttp"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ErrTokenCountUnavailable rejects an unverified routed input count. It does
// not grant physical Model dispatch, change a persisted invocation, or imply
// that a Provider request was sent.
var ErrTokenCountUnavailable = errors.New("provider input token count unavailable")

// ContextTokenCounter bridges Context's immutable canonical model request to
// a Host-composed model Client. It does not own a second provider registry.
// Only OpenAI Responses currently supplies a certified exact input count;
// Anthropic's documented estimate must not be treated as a hard upper bound.
type ContextTokenCounter struct {
	Client model.Client
}

var _ runtimecontext.TokenCounter = ContextTokenCounter{}

// Count checks both native model and provider identity BEFORE any HTTP. A
// canonical serialized request is decoded without permitting extra authority
// fields, then routed exactly as it would be for generation. Missing native
// counter capability is a safe error for Context's existing byte fallback.
func (counter ContextTokenCounter) Count(ctx context.Context, input runtimecontext.TokenCountRequest) (int64, error) {
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if counter.Client == nil || len(input.Payload) == 0 || len(input.Payload) > 8<<20 ||
		providerhttp.ValidateUniqueJSONKeys(input.Payload) != nil {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	var request model.Request
	decoder := json.NewDecoder(bytes.NewReader(input.Payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.Model == "" {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	// The Context Manager supplies model.MarshalRequestForInputCount bytes.
	// Generic key reordering would mutate signed Tool argument RawMessages,
	// so reject any alias, extra/trailing document or lossy decode through
	// an exact typed round-trip instead. This preserves the physical
	// generation request's frozen replay bytes for native counting.
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	encoded, err := model.MarshalRequestForInputCount(request)
	if err != nil || !bytes.Equal(input.Payload, encoded) {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	if input.Context.Protocol != "openai" || input.Context.Model == "" {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	client := counter.Client
	if router, ok := client.(*Router); ok {
		selected, routed, name, err := router.route(request)
		if err != nil || name != input.Context.Protocol || routed.Model != input.Context.Model {
			return 0, tokenCountFailure(InputCountLocalRejected)
		}
		client, request = selected, routed
	} else {
		named, ok := client.(model.ProviderNamer)
		if !ok || named.ProviderName() != input.Context.Protocol || request.Model != input.Context.Model {
			return 0, tokenCountFailure(InputCountLocalRejected)
		}
	}
	counting, ok := client.(model.InputTokenCounter)
	if !ok {
		return 0, tokenCountFailure(InputCountLocalRejected)
	}
	count, err := counting.CountInputTokens(ctx, model.CloneRequest(request))
	if err != nil {
		// A caller cancellation is not an observed native HTTP rejection.
		// Preserve the original typed cancellation and record it as other.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return 0, err
		}
		return 0, tokenCountFailure(classifyNativeInputFailure(err))
	}
	if count <= 0 {
		return 0, tokenCountFailure(InputCountOther)
	}
	return count, nil
}
