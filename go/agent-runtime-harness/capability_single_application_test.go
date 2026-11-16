package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type applicationMaterializerFunc func(context.Context, CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error)

func (fn applicationMaterializerFunc) MaterializeCapability(ctx context.Context, req CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
	return fn(ctx, req)
}

var _ CapabilityInvocationMaterializer = applicationMaterializerFunc(nil)

func TestCapabilityMaterializationHasOnlyApplicationExecution(t *testing.T) {
	descriptor := CommandDescriptor{
		ID: "cmd", CapabilityKey: "app.file", DefinitionVersion: "v1",
		Source: CommandSourceApplication, ApplicationKind: "file",
		ExecutionClass: ExecutionApplication,
		InputSchema:    json.RawMessage(`{"type":"object","additionalProperties":false,"required":["value"],"properties":{"value":{"type":"integer"}}}`),
	}
	command := json.RawMessage(`{"commandID":"cmd","goal":"write one draft","arguments":{"value":7}}`)
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	output := json.RawMessage(`{"safe":true}`)
	called := 0
	handler := NewCapabilityInvocationToolHandler(applicationMaterializerFunc(func(_ context.Context, request CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
		called++
		if request.Descriptor.ID != descriptor.ID || request.Goal != "write one draft" ||
			string(request.Arguments) != `{"value":7}` || request.Actor != actor || request.Thread != thread {
			t.Fatalf("Host materializer input changed: %+v", request)
		}
		return &ApplicationCapabilitySpec{Input: output}, nil
	}))
	config := ConfigSnapshot{Commands: []CommandDescriptor{descriptor}}
	input, gotDescriptor, spec, err := handler.materializeCapability(t.Context(), actor, thread, config, command)
	if err != nil || called != 1 || input.CommandID != descriptor.ID ||
		gotDescriptor.ExecutionClass != ExecutionApplication || spec == nil ||
		!reflect.DeepEqual([]byte(spec.Input), []byte(output)) {
		t.Fatalf("the application-only contract did not preserve input: input=%+v descriptor=%+v spec=%+v err=%v", input, gotDescriptor, spec, err)
	}
	for _, tc := range []struct {
		name         string
		materializer applicationMaterializerFunc
		commands     []CommandDescriptor
		command      json.RawMessage
	}{
		{"nil_result", func(context.Context, CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
			return nil, nil
		}, config.Commands, command},
		{"host_rejected", func(context.Context, CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
			return nil, ErrInvalidRequest
		}, config.Commands, command},
		{"foreign_class", func(context.Context, CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
			return &ApplicationCapabilitySpec{Input: json.RawMessage(`{}`)}, nil
		}, []CommandDescriptor{func() CommandDescriptor {
			copy := descriptor
			copy.ExecutionClass = ExecutionAgent
			return copy
		}()}, command},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rejected := NewCapabilityInvocationToolHandler(tc.materializer)
			_, _, _, err := rejected.materializeCapability(t.Context(), actor, thread, ConfigSnapshot{Commands: tc.commands}, tc.command)
			if err == nil {
				t.Fatal("unknown or unsupported application materialization admitted")
			}
		})
	}
	var toolCalled bool
	invalid := NewCapabilityInvocationToolHandler(applicationMaterializerFunc(func(context.Context, CapabilityMaterializeRequest) (*ApplicationCapabilitySpec, error) {
		toolCalled = true
		return &ApplicationCapabilitySpec{}, nil
	}))
	_, _, _, err = invalid.materializeCapability(t.Context(), actor, thread, config,
		json.RawMessage(`{"commandID":"cmd","goal":"draft","arguments":{"value":"wrong type"}}`))
	if err == nil || toolCalled {
		t.Fatal("Host materializer executed before frozen Tool-schema rejection")
	}
}

type applicationToolReceiptStore struct {
	Store
	invocation Invocation
}

func (store applicationToolReceiptStore) GetInvocation(context.Context, string) (Invocation, error) {
	return store.invocation, nil
}

func TestCapabilityToolResultIsOnlyApplicationEvidence(t *testing.T) {
	tests := []struct {
		name   string
		class  ExecutionClass
		status InvocationStatus
		want   string
		reject bool
	}{
		{"completed", ExecutionApplication, InvocationCompleted, "committed", false},
		{"running", ExecutionApplication, InvocationRunning, tools.ReceiptDispositionPending, false},
		{"waiting", ExecutionApplication, InvocationWaitingInput, tools.ReceiptDispositionPending, false},
		{"failed", ExecutionApplication, InvocationFailed, "committed", false},
		{"foreign_completed", ExecutionAgent, InvocationCompleted, "", true},
		{"foreign_pending", ExecutionAgent, InvocationRunning, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			invocation := Invocation{ID: "inv", ExecutionClass: tc.class, Status: tc.status,
				OutputRefs: []HostRef{{Kind: "draft", ID: "answer"}}}
			runner := &Runner{store: applicationToolReceiptStore{invocation: invocation}}
			result, err := runner.capabilityToolResult(t.Context(), "command", "inv", "call")
			if tc.reject {
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("foreign executor accepted as original Application: %v", err)
				}
				return
			}
			if err != nil || result.Pending != nil ||
				result.Receipt.ExecutionID != "call" || result.Receipt.Disposition != tc.want {
				t.Fatalf("invalid original application receipt: %+v err=%v", result, err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(result.Content, &decoded); err != nil {
				t.Fatal(err)
			}
			if tc.status == InvocationCompleted {
				if _, ok := decoded["outputRefs"]; !ok {
					t.Fatal("completed Application lost its original Host output references")
				}
				if _, ok := decoded["result"]; ok {
					t.Fatal("completed Application incorrectly emitted a Kernel Run result")
				}
			}
		})
	}
}
