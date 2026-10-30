package main

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
)

func TestReplayFailureClassificationPrecedesGenericUnsupported(t *testing.T) {
	if got := failureCode(errors.Join(openai.ErrUnsupported, openai.ErrReplayUnavailable), "provider_failed"); got != "provider_replay_unavailable" {
		t.Fatalf("lost actionable replay diagnosis: %s", got)
	}
	if got := failureCode(openai.ErrUnsupported, "provider_failed"); got != "provider_unsupported" {
		t.Fatalf("changed generic unsupported classification: %s", got)
	}
}
