package workflowhttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

var errInvalidDefinitionDraftBody = errors.New("invalid required workflow definition draft")

// The draft is a value struct for backwards-compatible public Go DTOs.
// Gin's default binding validator ignores 'required' on value structs, and a
// zero revision is valid but indistinguishable from an omitted JSON key.
// Validate presence and field constraints against the original wire body.
func (request *CompileDefinitionRequest) ValidateStrictJSONBody(raw []byte) error {
	return validateRequiredDraftBody(raw, request.Draft)
}

func (request *PublishDefinitionRequest) ValidateStrictJSONBody(raw []byte) error {
	return validateRequiredDraftBody(raw, request.Draft)
}

func validateRequiredDraftBody(raw []byte, draft workflow.DefinitionDraft) error {
	var envelope struct {
		Draft json.RawMessage `json:"draft"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return errInvalidDefinitionDraftBody
	}
	var fields map[string]json.RawMessage
	if len(envelope.Draft) == 0 || json.Unmarshal(envelope.Draft, &fields) != nil || fields == nil {
		return errInvalidDefinitionDraftBody
	}
	for _, required := range []string{"id", "revision", "name", "nodes"} {
		field, present := fields[required]
		if !present || len(field) == 0 || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return errInvalidDefinitionDraftBody
		}
	}
	if strings.TrimSpace(draft.ID) == "" || utf8.RuneCountInString(draft.ID) > 255 ||
		draft.Revision < 0 || strings.TrimSpace(draft.Name) == "" ||
		utf8.RuneCountInString(draft.Name) > 1000 || len(draft.Nodes) == 0 {
		return errInvalidDefinitionDraftBody
	}
	return nil
}
