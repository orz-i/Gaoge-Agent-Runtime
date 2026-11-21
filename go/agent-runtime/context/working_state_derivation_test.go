package context_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

func summaryDerivation() *runtimectx.WorkingStateDerivation {
	return &runtimectx.WorkingStateDerivation{
		Kind: runtimectx.WorkingStateDerivationSummary, RunID: "summary-source-run",
		InvocationID: "modelinv_" + strings.Repeat("a", 32), RequestHash: strings.Repeat("b", 64),
		PolicyVersion: "working-state-summary-v2",
	}
}

func TestWorkingStateDerivationIsHashedCarriedAndCannotBeSubmittedByHost(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	previous, err := manager.Open(t.Context(), openRequest("u1", "a1", "u2"))
	requireNoError(t, err)
	window := runtimectx.CloneWindow(previous.Window)
	window.WorkingState = workingStateFor(previous, 1)
	window.WorkingState.Derivation = summaryDerivation()
	automatic, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: previous, Window: window, Reason: "working_state_summary",
	})
	requireNoError(t, err)
	if !runtimectx.ValidCheckpoint(automatic) || automatic.ContentHash == previous.ContentHash {
		t.Fatal("Runtime derivation was not sealed in a valid checkpoint")
	}
	window.WorkingState.Derivation.RunID = "caller mutation"
	if automatic.Window.WorkingState.Derivation.RunID != "summary-source-run" {
		t.Fatal("rollover retained mutable derivation storage")
	}
	cloned := runtimectx.CloneCheckpoint(automatic)
	cloned.Window.WorkingState.Derivation.RequestHash = strings.Repeat("c", 64)
	if !runtimectx.ValidCheckpoint(automatic) || runtimectx.ValidCheckpoint(cloned) {
		t.Fatal("cloning aliased derivation or derivation escaped the Window hash")
	}
	request := openRequest("u1", "a1", "u2", "a2", "u3")
	request.Previous = &automatic
	carried, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if !reflect.DeepEqual(carried.Window.WorkingState, automatic.Window.WorkingState) {
		t.Fatal("nil source append dropped the already-committed automatic origin")
	}
	request.Previous = &carried
	request.WorkingState = workingStateFor(carried, 2)
	request.WorkingState.Derivation = summaryDerivation()
	if _, err := manager.Open(t.Context(), request); !errors.Is(err, runtimectx.ErrInvalidInput) {
		t.Fatalf("explicit Host update impersonated the Runtime origin: %v", err)
	}
	request.WorkingState.Derivation = nil
	manual, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if manual.Window.WorkingState.Derivation != nil || manual.Window.WorkingState.Version != 2 {
		t.Fatal("a higher-version manual replacement retained automatic provenance")
	}
}

func TestWorkingStateDerivationRejectsUnboundedOrMalformedLocators(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	previous, err := manager.Open(t.Context(), openRequest("u1"))
	requireNoError(t, err)
	for name, change := range map[string]func(*runtimectx.WorkingStateDerivation){
		"unknown kind": func(d *runtimectx.WorkingStateDerivation) { d.Kind = "host_claim" },
		"empty run":    func(d *runtimectx.WorkingStateDerivation) { d.RunID = "" },
		"padded run":   func(d *runtimectx.WorkingStateDerivation) { d.RunID = " run" },
		"large run": func(d *runtimectx.WorkingStateDerivation) {
			d.RunID = strings.Repeat("r", runtimectx.MaxWorkingStateTextBytes+1)
		},
		"invalid invocation":   func(d *runtimectx.WorkingStateDerivation) { d.InvocationID = "modelinv_short" },
		"uppercase invocation": func(d *runtimectx.WorkingStateDerivation) { d.InvocationID = "modelinv_" + strings.Repeat("A", 32) },
		"bad hash":             func(d *runtimectx.WorkingStateDerivation) { d.RequestHash = strings.Repeat("z", 64) },
		"empty policy":         func(d *runtimectx.WorkingStateDerivation) { d.PolicyVersion = "" },
		"padded policy":        func(d *runtimectx.WorkingStateDerivation) { d.PolicyVersion = " policy" },
		"large policy":         func(d *runtimectx.WorkingStateDerivation) { d.PolicyVersion = strings.Repeat("v", 129) },
		"invalid UTF8":         func(d *runtimectx.WorkingStateDerivation) { d.RunID = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			state := workingStateFor(previous, 1)
			state.Derivation = summaryDerivation()
			change(state.Derivation)
			if err := runtimectx.ValidateWorkingState(state); !errors.Is(err, runtimectx.ErrInvalidInput) {
				t.Fatalf("malformed derivation accepted: %v", err)
			}
		})
	}
}
