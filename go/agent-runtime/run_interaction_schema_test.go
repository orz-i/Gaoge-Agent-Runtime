package agentruntime

import (
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueRevise658889E8 = "revise"
)

const (
	valueAction41ED716E   = "action"
	valueAnswerE9B841AE   = "answer"
	valueApprove568E4C26  = "approve"
	valueFeedbackF55C7CC9 = "feedback"
	valueTrueC6ECD702     = "true"
)

func TestRunInteractionResponseSchemasRejectInvalidPayloads(t *testing.T) {
	tests := []struct {
		name, kind string
		response   map[string]interface{}
	}{
		{"tool missing", model.InteractionApproveTool, map[string]interface{}{}},
		{"tool wrong type", model.InteractionApproveTool, map[string]interface{}{"approved": valueTrueC6ECD702}},
		{"plan enum", model.InteractionSubmitPlan, map[string]interface{}{valueAction41ED716E: "edit"}},
		{"step extra", model.InteractionApproveStep, map[string]interface{}{valueAction41ED716E: valueApprove568E4C26, "extra": true}},
		{"ask empty", model.InteractionAskUser, map[string]interface{}{valueAnswerE9B841AE: ""}},
		{"ask too long", model.InteractionAskUser, map[string]interface{}{valueAnswerE9B841AE: strings.Repeat("答", 20001)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateRunInteractionResponse(runInteractionResponseSchema(test.kind), test.response)
			if !errors.Is(err, ErrRunInteractionResponseInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunInteractionResponseSchemasAcceptValidPayloads(t *testing.T) {
	tests := []struct {
		kind     string
		response map[string]interface{}
	}{
		{model.InteractionApproveTool, map[string]interface{}{"approved": false}},
		{model.InteractionSubmitPlan, map[string]interface{}{valueAction41ED716E: valueRevise658889E8, valueFeedbackF55C7CC9: "补充依赖"}},
		{model.InteractionApproveStep, map[string]interface{}{valueAction41ED716E: valueApprove568E4C26}},
		{model.InteractionAskUser, map[string]interface{}{valueAnswerE9B841AE: "继续"}},
	}
	for _, test := range tests {
		if err := validateRunInteractionResponse(runInteractionResponseSchema(test.kind), test.response); err != nil {
			t.Fatalf("kind %s: %v", test.kind, err)
		}
	}
}

func TestRunInteractionPersistedSchemaMustUseSupportedStrictSubset(t *testing.T) {
	for _, schema := range []string{
		`not-json`,
		`{"type":"array"}`,
		`{"type":"object","properties":{},"patternProperties":{}}`,
		`{"type":"object","properties":{}}`,
		`{"type":"object","properties":{"optional":{"type":"string","pattern":"x"}},"additionalProperties":false}`,
	} {
		err := validateRunInteractionResponse(schema, map[string]interface{}{})
		if !errors.Is(err, ErrRunInteractionSchemaIncompatible) {
			t.Fatalf("schema %q: %v", schema, err)
		}
	}
}
