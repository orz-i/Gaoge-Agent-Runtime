package plugin_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

const (
	testPluginFirst  = "first"
	testPluginSecond = "second"
)

var errPolicyFailed = errors.New("policy failed")

type approvalPolicy struct {
	name   string
	result plugin.ApprovalRequirement
	err    error
	order  *[]string
}

func (policy approvalPolicy) Name() string { return policy.name }

func (policy approvalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	if policy.order != nil {
		*policy.order = append(*policy.order, policy.name)
	}
	return policy.result, policy.err
}

func TestApprovalPolicySetEvaluatesAllPoliciesAndRequiresAny(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	set, err := plugin.NewApprovalPolicySet(
		approvalPolicy{name: testPluginFirst, result: plugin.ApprovalRequired, order: &order},
		approvalPolicy{name: testPluginSecond, result: plugin.ApprovalNotRequired, order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	required, err := set.RequiresApproval(t.Context(), plugin.ToolInvocation{})
	if err != nil || !required || len(order) != 2 || order[0] != testPluginFirst || order[1] != testPluginSecond {
		t.Fatalf("required=%v order=%#v err=%v", required, order, err)
	}
}

func TestApprovalPolicySetFailsClosedOnPolicyError(t *testing.T) {
	t.Parallel()
	set, err := plugin.NewApprovalPolicySet(
		approvalPolicy{name: "required", result: plugin.ApprovalRequired},
		approvalPolicy{name: "broken", err: errPolicyFailed},
	)
	if err != nil {
		t.Fatal(err)
	}
	required, err := set.RequiresApproval(t.Context(), plugin.ToolInvocation{})
	if required || !errors.Is(err, errPolicyFailed) {
		t.Fatalf("required=%v err=%v", required, err)
	}
}

type mutatingObserver struct {
	name       string
	order      *[]string
	panicAfter bool
}

func (observer mutatingObserver) Name() string { return observer.name }

func (observer mutatingObserver) Observe(_ context.Context, event plugin.Event) {
	*observer.order = append(*observer.order, observer.name)
	if len(event.Data) > 0 {
		event.Data[0] = '['
	}
	if observer.panicAfter {
		panic("observer failure")
	}
}

func TestObserverSetPreservesOrderIsolationAndContainsPanics(t *testing.T) {
	t.Parallel()
	order := make([]string, 0, 2)
	set, err := plugin.NewObserverSet(
		mutatingObserver{name: testPluginFirst, order: &order, panicAfter: true},
		mutatingObserver{name: testPluginSecond, order: &order},
	)
	if err != nil {
		t.Fatal(err)
	}
	event := plugin.Event{RunID: "run-1", Type: "test", Data: []byte(`{"ok":true}`)}
	set.Observe(t.Context(), event)
	if len(order) != 2 || order[0] != testPluginFirst || order[1] != testPluginSecond {
		t.Fatalf("observer order = %#v", order)
	}
	if string(event.Data) != `{"ok":true}` {
		t.Fatalf("observer mutated source event: %s", event.Data)
	}
}

func TestExplicitPluginSetsRejectDuplicateNames(t *testing.T) {
	t.Parallel()
	if _, err := plugin.NewApprovalPolicySet(approvalPolicy{name: "guard"}, approvalPolicy{name: " guard "}); !errors.Is(err, plugin.ErrDuplicateName) {
		t.Fatalf("approval duplicate error = %v", err)
	}
	order := []string{}
	if _, err := plugin.NewObserverSet(mutatingObserver{name: "observer", order: &order}, mutatingObserver{name: " observer ", order: &order}); !errors.Is(err, plugin.ErrDuplicateName) {
		t.Fatalf("observer duplicate error = %v", err)
	}
}
