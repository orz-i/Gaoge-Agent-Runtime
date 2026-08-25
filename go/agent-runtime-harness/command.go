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

// CommandDescriptor is a discoverable, data-only projection of one explicit
// Harness capability entry point. It never contains an executable callback.
type CommandDescriptor struct {
	ID                string          `json:"id"`
	Trigger           string          `json:"trigger"`
	Title             string          `json:"title"`
	Description       string          `json:"description,omitempty"`
	CapabilityKey     string          `json:"capabilityKey"`
	DefinitionVersion string          `json:"definitionVersion"`
	ExecutionClass    ExecutionClass  `json:"executionClass"`
	Source            string          `json:"source"`
	ApplicationKind   string          `json:"applicationKind,omitempty"`
	InputSchema       json.RawMessage `json:"inputSchema"`
}

// CommandCatalog is an immutable static projection assembled by the bootstrap
// composition root. Lookup is intentionally slice-based rather than a runtime
// registration mechanism.
type CommandCatalog struct{ descriptors []CommandDescriptor }

// NewCommandCatalog validates and freezes the exact statically composed set.
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
		if values[index-1].ID == values[index].ID || values[index-1].Trigger == values[index].Trigger {
			return nil, ErrConflict
		}
	}
	return &CommandCatalog{descriptors: values}, nil
}

// List returns an isolated deterministic command projection.
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

// Resolve returns one exact descriptor by stable command ID.
func (catalog *CommandCatalog) Resolve(id string) (CommandDescriptor, error) {
	id = strings.TrimSpace(id)
	if catalog == nil || id == "" {
		return CommandDescriptor{}, ErrInvalidRequest
	}
	for _, descriptor := range catalog.descriptors {
		if descriptor.ID == id {
			return cloneCommandDescriptor(descriptor), nil
		}
	}
	return CommandDescriptor{}, ErrNotFound
}

// FirstPartyCommandDescriptors declares only the three built-in executable
// capability entries. Application contributions are appended explicitly by
// bootstrap composition in later phases.
func FirstPartyCommandDescriptors() []CommandDescriptor {
	const noArguments = `{"type":"object","additionalProperties":false}`
	const workflowArguments = `{"type":"object","properties":{"definitionReference":{"type":"object","required":["id"],"properties":{"id":{"type":"string","minLength":1},"revision":{"type":"integer","minimum":1},"hash":{"type":"string","minLength":1}},"additionalProperties":false},"input":{}},"additionalProperties":false}`
	return []CommandDescriptor{
		{
			ID: "plan", Trigger: "/plan", Title: "Plan", Description: "Create and execute a bounded plan",
			CapabilityKey: CapabilityPlanExecute, DefinitionVersion: RuntimeCapabilityVersion,
			ExecutionClass: ExecutionPlanExecute, Source: CommandSourceFirstParty, InputSchema: json.RawMessage(noArguments),
		},
		{
			ID: "team", Trigger: "/team", Title: "Team", Description: "Run a small parallel specialist team",
			CapabilityKey: CapabilityTeam, DefinitionVersion: RuntimeCapabilityVersion,
			ExecutionClass: ExecutionTeam, Source: CommandSourceFirstParty, InputSchema: json.RawMessage(noArguments),
		},
		{
			ID: "workflow", Trigger: "/workflow", Title: "Workflow", Description: "Run a bounded dynamic workflow",
			CapabilityKey: CapabilityWorkflow, DefinitionVersion: RuntimeCapabilityVersion,
			ExecutionClass: ExecutionWorkflow, Source: CommandSourceFirstParty, InputSchema: json.RawMessage(workflowArguments),
		},
	}
}

func normalizeCommandDescriptor(value CommandDescriptor) (CommandDescriptor, error) {
	value.ID = strings.TrimSpace(value.ID)
	value.Trigger = strings.TrimSpace(value.Trigger)
	value.Title = strings.TrimSpace(value.Title)
	value.Description = strings.TrimSpace(value.Description)
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
	return value.ID != "" && value.Title != "" && value.CapabilityKey != "" && value.DefinitionVersion != "" &&
		strings.HasPrefix(value.Trigger, "/") && !strings.ContainsAny(value.Trigger, " \t\r\n")
}

func validCommandExecution(value CommandDescriptor) bool {
	if !validExecutionClass(value.ExecutionClass) {
		return false
	}
	switch value.Source {
	case CommandSourceFirstParty:
		return value.ApplicationKind == ""
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
