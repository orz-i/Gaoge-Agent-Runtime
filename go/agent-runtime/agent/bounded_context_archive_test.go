package agent

import (
	"encoding/json"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

func TestManagedContextNeverOffersProviderReplayAsReadableArchive(t *testing.T) {
	signed := []runtimecontext.Artifact{{ContentJSON: json.RawMessage(`[{"role":"assistant","content":[{"kind":"text","text":"old"}],"providerReplay":{"provider":"anthropic","format":"signed","data":{"signature":"private-signed-opaque"}}}]`)}}
	if !boundedArchivesContainProviderReplay(signed) {
		t.Fatal("signed provider continuation would be exposed by archival reader")
	}
	safe := []runtimecontext.Artifact{{ContentJSON: json.RawMessage(`[{"role":"assistant","content":[{"kind":"text","text":"old"}]}]`)}}
	if boundedArchivesContainProviderReplay(safe) {
		t.Fatal("ordinary text would be needlessly blocked from archival compaction")
	}
}
