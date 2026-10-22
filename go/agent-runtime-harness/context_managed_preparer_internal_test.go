package harness

import (
	"encoding/json"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
)

func TestManagedContextNeverOffersProviderReplayAsReadableArchive(t *testing.T) {
	signed := []runtimecontext.Artifact{{ContentJSON: json.RawMessage(`[{"role":"assistant","content":"old","providerReplay":{"provider":"anthropic","format":"signed","data":{"signature":"private-signed-opaque"}}}]`)}}
	if !archivesContainProviderReplay(signed) {
		t.Fatal("signed provider continuation would be exposed by archival reader")
	}
	safe := []runtimecontext.Artifact{{ContentJSON: json.RawMessage(`[{"role":"assistant","content":"old"}]`)}}
	if archivesContainProviderReplay(safe) {
		t.Fatal("ordinary text would be needlessly blocked from archival compaction")
	}
}
