package continuation

import (
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

// ResumerRegistration binds exactly one Runtime kind to its continuation surface.
// It is a dispatcher-only routing entry, not a general service locator.
type ResumerRegistration struct {
	kind    kernel.RunKind
	resumer Resumer
}

// RegisterResumer creates one explicit continuation resumption binding.
func RegisterResumer(kind kernel.RunKind, resumer Resumer) ResumerRegistration {
	return ResumerRegistration{kind: kind, resumer: resumer}
}

// SelfTriggerResolver maps one feature-owned durable event into a continuation trigger.
type SelfTriggerResolver func(kernel.EventDraft) (Trigger, bool)

// TriggerRegistration binds self-trigger semantics to one Runtime kind.
type TriggerRegistration struct {
	kind     kernel.RunKind
	resolver SelfTriggerResolver
}

// RegisterTriggers creates one explicit feature self-trigger binding.
func RegisterTriggers(kind kernel.RunKind, resolver SelfTriggerResolver) TriggerRegistration {
	return TriggerRegistration{kind: kind, resolver: resolver}
}

func validRegistrationKind(kind kernel.RunKind) bool {
	value := string(kind)
	return value != "" && strings.TrimSpace(value) == value
}
