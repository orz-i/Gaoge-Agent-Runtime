package main

import (
	"bytes"
	"context"
	"testing"
)

// run verifies both immediate Prompt replay with unchanged admission and
// delayed Load of turn-1 after turn-2's answer is appended. It also proves
// that a late Prompt with changed source history fails closed without a send.
func TestMinimalSessionDelayedReplayLoadsOriginalAndRejectsChangedHistory(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "turn-1: I can see 1 user turn(s).\nturn-2: I can see 2 user turn(s).\n"
	if got := output.String(); got != want {
		t.Fatalf("unexpected model-visible conversation:\ngot:  %q\nwant: %q", got, want)
	}
}
