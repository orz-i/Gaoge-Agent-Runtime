package agent

import (
	"context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"testing"
)

func TestPhysicalObservationNestedRunIsolationAndSameTargetRejection(t *testing.T) {
	r := &Runner{}
	op := plugin.RunOperation("resume")
	for _, mode := range []string{"different-run", "duplicate", "wrong-runner", "wrong-operation"} {
		t.Run(mode, func(t *testing.T) {
			o := &physicalObservation{runID: "parent", operation: op, runner: r}
			ctx := context.WithValue(t.Context(), physicalObservationKey{}, o)
			parent := r.claimPhysicalObservation(ctx, "parent", op)
			switch mode {
			case "different-run":
				child := r.claimPhysicalObservation(parent, "child", op)
				recordPhysicalObservation(child, "parent", PhysicalCallSettled)
				if o.kind != PhysicalCallUnknown || o.invalid {
					t.Fatal("child inherited or polluted parent observation")
				}
			case "duplicate":
				r.claimPhysicalObservation(parent, "parent", op)
			case "wrong-runner":
				other := &Runner{}
				other.claimPhysicalObservation(parent, "parent", op)
			case "wrong-operation":
				r.claimPhysicalObservation(parent, "parent", plugin.RunOperation("start"))
			}
			recordPhysicalObservation(parent, "parent", PhysicalCallSettled)
			if mode == "different-run" {
				if o.kind != PhysicalCallSettled || o.invalid {
					t.Fatal("parent original claim lost")
				}
			} else if !o.invalid || o.kind != PhysicalCallUnknown {
				t.Fatal("same-target reused claim")
			}
		})
	}
}
