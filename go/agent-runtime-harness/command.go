package harness

import (
	"encoding/json"
	"slices"
	"strings"
)

const (
	CommandSourceFirstParty  = "first_party"
	CommandSourceApplication = "application"
)

// CommandDescriptor is a frozen authorization and input contract for one
// explicitly host-selected child capability. Host presentation (slash
// triggers, labels, descriptions and intent selection) is not Runtime state.
type CommandDescriptor struct {
	ID                string          `json:"id"`
	CapabilityKey     string          `json:"capabilityKey"`
	DefinitionVersion string          `json:"definitionVersion"`
	ExecutionClass    ExecutionClass  `json:"executionClass"`
	Source            string          `json:"source"`
	ApplicationKind   string          `json:"applicationKind,omitempty"`
	InputSchema       json.RawMessage `json:"inputSchema"`
}

// CommandCatalog is a frozen set of executable capability contracts, not a
// Host command registry or user-intent router.
type CommandCatalog struct{ descriptors []CommandDescriptor }

// UnmarshalJSON rejects legacy Host presentation fields rather than silently
// stripping them when old persisted configs are loaded after the hard cut.
// The authoritative frozen execution descriptor is not a display command.
func (value *CommandDescriptor) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ErrInvalidRequest
	}
	for key := range fields {
		switch key {
		case "id", "capabilityKey", "definitionVersion", "executionClass", "source", "applicationKind", "inputSchema":
		default:
			return ErrInvalidRequest
		}
	}
	type wire CommandDescriptor
	var decoded wire
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return ErrInvalidRequest
	}
	*value = CommandDescriptor(decoded)
	return nil
}

// NewCommandCatalog validates and freezes the exact host-selected set.
func NewCommandCatalog(descriptors ...CommandDescriptor) (*CommandCatalog, error) {
	values := make([]CommandDescriptor, len(descriptors))
	for index, descriptor := range descriptors {
		normalized, err := normalizeCommandDescriptor(descriptor)
		if err != nil {
			return nil, err
		}
		values[index] = normalized
	}
	slices.SortFunc(values, func(left, right CommandDescriptor) int {
		return strings.Compare(left.ID, right.ID)
	})
	for index := 1; index < len(values); index++ {
		if values[index-1].ID == values[index].ID {
			return nil, ErrConflict
		}
	}
	return &CommandCatalog{descriptors: values}, nil
}

// List returns an isolated deterministic snapshot of execution contracts.
func (catalog *CommandCatalog) List() []CommandDescriptor {
	if catalog == nil {
		return nil
	}
	result := make([]CommandDescriptor, len(catalog.descriptors))
	for index, value := range catalog.descriptors {
		result[index] = cloneCommandDescriptor(value)
	}
	return result
}

func normalizeCommandDescriptor(value CommandDescriptor) (CommandDescriptor, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.CapabilityKey = strings.TrimSpace(value.CapabilityKey)
	value.DefinitionVersion = strings.TrimSpace(value.DefinitionVersion)
	value.Source = strings.TrimSpace(value.Source)
	value.ApplicationKind = strings.TrimSpace(value.ApplicationKind)
	if !validCommandIdentity(value) || !validCommandExecution(value) || !validCommandSchema(value.InputSchema) {
		return CommandDescriptor{}, ErrInvalidRequest
	}
	value.InputSchema = append(json.RawMessage(nil), value.InputSchema...)
	return value, nil
}

func validCommandIdentity(value CommandDescriptor) bool {
	return value.ID != "" && value.CapabilityKey != "" && value.DefinitionVersion != "" &&
		!strings.ContainsAny(value.ID, " \t\r\n")
}

func validCommandExecution(value CommandDescriptor) bool {
	switch value.Source {
	case CommandSourceFirstParty:
		if value.ApplicationKind != "" {
			return false
		}
		// These are the only first-party child capabilities supported by
		// the typed materializer. Agent/GroupChat are not implicit routes.
		return value.ExecutionClass == ExecutionTeam && value.CapabilityKey == CapabilityTeam ||
			value.ExecutionClass == ExecutionWorkflow && value.CapabilityKey == CapabilityWorkflow
	case CommandSourceApplication:
		return value.ExecutionClass == ExecutionApplication && value.ApplicationKind != ""
	default:
		return false
	}
}

func validCommandSchema(value json.RawMessage) bool { return len(value) > 0 && json.Valid(value) }

func cloneCommandDescriptor(value CommandDescriptor) CommandDescriptor {
	value.InputSchema = append(json.RawMessage(nil), value.InputSchema...)
	return value
}

func cloneCommandDescriptors(values []CommandDescriptor) []CommandDescriptor {
	result := make([]CommandDescriptor, len(values))
	for index, value := range values {
		result[index] = cloneCommandDescriptor(value)
	}
	return result
}
