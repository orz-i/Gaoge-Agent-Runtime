package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunnableHostSteeringAndLateInputFallback(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		var output bytes.Buffer
		err := run(ctx, strings.NewReader(correction+"\n"), &output, interactive)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"queued=1 applied=0", "applied=1 answer=beta: 18:00", "late_input=new_turn answer=alpha: 09:00", "fixture_only=true persistence=memory"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("missing user-visible result %q: %s", want, output.String())
			}
		}
	}
}

func TestInteractiveDemoDoesNotPretendInvalidInputWasApplied(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := run(ctx, strings.NewReader("different unsupported fixture input\n"), &output, true); err == nil {
		t.Fatal("invalid fixture input was accepted")
	}
	if strings.Contains(output.String(), "applied=1") {
		t.Fatal("invalid input was reported as applied")
	}
}
