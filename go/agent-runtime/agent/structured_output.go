package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var ErrOutputSchema = errors.New("terminal model output violates strict JSON Schema")
var ErrOutputRefused = errors.New("model refused strict structured output")

// The capability decision happens before Run creation, and a second check in
// the Provider protects direct callers and changed Provider configurations.
// A live Run stores the immutable schema. Unknown model capability is never
// silently replaced by prompt-only JSON mode.
func (runner *Runner) validateOutputCapability(ctx context.Context, modelName string, contract *model.OutputContract) error {
	if contract == nil {
		return nil
	}
	if err := model.ValidateOutputContract(contract); err != nil {
		return errors.Join(ErrInvalidRequest, err)
	}
	if strings.TrimSpace(modelName) == "" || runner == nil || runner.model == nil {
		return errors.Join(ErrInvalidRequest, model.ErrCapabilityUnavailable)
	}
	capability, ok := runner.model.(model.CapabilityProvider)
	if !ok {
		return errors.Join(ErrInvalidRequest, model.ErrCapabilityUnavailable)
	}
	available, err := capability.Capabilities(ctx, modelName)
	if err != nil {
		return errors.Join(ErrInvalidRequest, err)
	}
	if !available.StructuredJSONSchema {
		return errors.Join(ErrInvalidRequest, model.ErrCapabilityUnavailable)
	}
	return nil
}

// Validate against the exact Run-frozen schema, never a later Host lookup.
// Return only a stable error code; do not leak raw untrusted model output.
func validateStructuredDocument(contract *model.OutputContract, raw []byte) error {
	if contract == nil {
		return nil
	}
	validator, err := jsoncontract.Compile(contract.Schema)
	if err != nil {
		return ErrOutputSchema
	}
	if !json.Valid(raw) || validator.Validate(json.RawMessage(raw)) != nil {
		return ErrOutputSchema
	}
	return nil
}

func matchesStructuredDocument(contract *model.OutputContract, raw []byte) bool {
	return validateStructuredDocument(contract, raw) == nil
}

func structuredOutputCorrection(contract *model.OutputContract, response model.Response) (CompletionCorrection, error) {
	if contract == nil {
		return CompletionCorrection{}, nil
	}
	if response.Refused {
		return CompletionCorrection{}, ErrOutputRefused
	}
	if len(response.Artifacts) > 0 || len(response.HostedToolCalls) > 0 || len(response.Citations) > 0 {
		return CompletionCorrection{Code: "agent.output_schema_invalid", Message: "The final answer must contain only the JSON document required by the frozen output schema."}, nil
	}
	if !matchesStructuredDocument(contract, []byte(response.Content)) {
		return CompletionCorrection{Code: "agent.output_schema_invalid", Message: "The final answer must satisfy the frozen strict JSON output schema."}, nil
	}
	return CompletionCorrection{}, nil
}
