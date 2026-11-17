package a2a

import (
	"encoding/json"
	"errors"
	"testing"
)

// Shadow target identity remains a runtime authorization prerequisite after
// removing the unrelated Workbench topology projection.
func TestDecodeShadowIdentityStateFailsClosedWithoutRemoteBinding(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`{}`,
		`{"remoteName":"agent","protocolVersion":"1.0"}`,
		`{"remoteURL":"https://agent.example","protocolVersion":"1.0"}`,
	} {
		if _, err := decodeShadowIdentityState(json.RawMessage(raw)); !errors.Is(err, ErrRemoteBindingChanged) {
			t.Fatalf("unbound remote shadow was accepted (%s): %v", raw, err)
		}
	}
	state, err := decodeShadowIdentityState(json.RawMessage(
		`{"remoteName":"agent","remoteURL":"https://agent.example","protocolVersion":"1.0","targetID":"remote","targetRevision":"v1"}`,
	))
	if err != nil || state.TargetID != "remote" || state.TargetRevision != "v1" {
		t.Fatalf("valid frozen shadow identity not decoded: %+v %v", state, err)
	}
}
