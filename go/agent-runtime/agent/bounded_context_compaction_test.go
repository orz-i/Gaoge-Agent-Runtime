package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestBoundedContextCompactionCandidateBudgetMatrix(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		hard                             int64
		soft                             int
		preferred, minimum               int
		wantFound, wantExtract, wantSoft bool
		wantRemoved                      int
	}{
		{"soft pressure readable fits", 12000, 30, 1, 1, true, true, true, 4},
		{"soft pressure archive only fits", 12000, 20, 1, 1, true, false, true, 4},
		{"soft pressure neither meets target", 12000, 1, 1, 1, false, false, false, 0},
		{"hard pressure readable fits soft", 4000, 80, 1, 1, true, true, true, 4},
		{"hard pressure archive only fits hard", 2000, 20, 1, 1, true, false, false, 4},
		{"hard pressure archive only fits soft", 3000, 30, 1, 1, true, false, true, 4},
		{"hard pressure no admissible candidate", 500, 80, 1, 1, false, false, false, 0},
		{"soft pressure cannot shrink preferred tail", 12000, 20, 2, 1, false, false, false, 0},
		{"hard pressure may shrink to protected tail", 5000, 30, 2, 1, true, false, true, 4},
		{"hard pressure existing hard candidate cannot stop protected-tail search", 8000, 20, 2, 1, true, false, true, 4},
		{"hard pressure cannot shrink protected tail", 2000, 80, 2, 2, false, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
			ids := []string{"u1", "a1", "u2", "a2", "u3"}
			entries := make([]runtimecontext.Entry, 0, len(ids))
			for i, id := range ids {
				role := model.RoleUser
				if i%2 == 1 {
					role = model.RoleAssistant
				}
				content := "current"
				if i < 4 {
					content = strings.Repeat("history_", 256)
				}
				entries = append(entries, runtimecontext.Entry{
					ID: "entry-" + id, SourceID: id, TurnID: "turn-" + id,
					Message: model.Message{Role: role, Content: model.TextParts(content)},
				})
			}
			previous, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
				ScopeID: "conversation:probe", StaticFingerprint: runtimecontext.StaticFingerprint("instructions", "tools"),
				SourcePath: ids, Entries: entries, Instructions: "system instructions",
			})
			if err != nil {
				t.Fatal(err)
			}
			policy := runtimecontext.NormalizePolicy(runtimecontext.Policy{
				PreserveRecentTurns: tc.preferred, MinimumRecentTurns: tc.minimum,
				MaxCompactionTokens: 512, MaxInputTokens: tc.hard, SoftLimitPercent: tc.soft, MaxSerializedBytes: 1 << 30,
			})
			request := model.Request{RunID: "run-probe", Model: "openai/example", Messages: runtimecontext.Materialize(previous.Window)}
			window := runtimecontext.ModelWindow{ContextTokens: 30000, ReservedOutputTokens: 1024}
			original, err := manager.AssessModelRequest(t.Context(), request, window, policy)
			if err != nil {
				t.Fatal(err)
			}
			if original.HardTokenEstimate <= original.SoftInputTokens {
				t.Fatalf("fixture has no pressure: %+v", original)
			}
			adapter := &BoundedContextAdapter{manager: manager}
			selected, found, err := adapter.selectCompaction(t.Context(), previous, request, window, policy, original, runtimecontext.WithinHardBudget(original, policy))
			if err != nil || found != tc.wantFound {
				t.Fatalf("found=%v want=%v original=%+v err=%v", found, tc.wantFound, original, err)
			}
			if !found {
				return
			}
			if !runtimecontext.WithinHardBudget(selected.assessment, policy) ||
				selected.assessment.HardTokenEstimate >= original.HardTokenEstimate ||
				selected.assessment.SerializedBytes >= original.SerializedBytes ||
				(selected.assessment.HardTokenEstimate <= selected.assessment.SoftInputTokens) != tc.wantSoft {
				t.Fatalf("unexpected candidate budget: %+v original=%+v", selected.assessment, original)
			}
			active := runtimecontext.Materialize(selected.window)
			if !reflect.DeepEqual(active[len(active)-1], request.Messages[len(request.Messages)-1]) {
				t.Fatal("protected current user message changed")
			}
			hasExtract := false
			for _, message := range active {
				if strings.Contains(model.TextValue(message.Content), "<context_checkpoint_extract>") {
					hasExtract = true
				}
			}
			if hasExtract != tc.wantExtract {
				t.Fatalf("readable extract=%v want=%v", hasExtract, tc.wantExtract)
			}
			var archived []model.Message
			for _, artifact := range selected.artifacts {
				var messages []model.Message
				if !runtimecontext.ValidArtifact(artifact) || json.Unmarshal(artifact.ContentJSON, &messages) != nil {
					t.Fatal("invalid exact archive")
				}
				archived = append(archived, messages...)
			}
			archivedJSON, archiveErr := json.Marshal(archived)
			originalJSON, originalErr := json.Marshal(request.Messages[1 : 1+tc.wantRemoved])
			if len(archived) != tc.wantRemoved || archiveErr != nil || originalErr != nil || string(archivedJSON) != string(originalJSON) {
				t.Fatalf("archive did not preserve exact removed messages: count=%d want=%d", len(archived), tc.wantRemoved)
			}
		})
	}
}
