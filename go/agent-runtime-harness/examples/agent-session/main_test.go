package main

import (
	"bytes"
	"testing"
)

func TestQuickstartUsesToolAndContinuesHostConversation(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "release-1: Release checks: 12 passed, 0 failed.\nrelease-2: Previous answer: Release checks: 12 passed, 0 failed.\n"
	if got := output.String(); got != want {
		t.Fatalf("quickstart did not carry the Tool-backed answer into the next Turn: %q", got)
	}
}
