package harness

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
)

const parentGoal = "parent goal"

func TestMergeContextRuntimeMessagesDirectGoalDoesNotDuplicateCurrentTurn(t *testing.T) {
	t.Parallel()
	contextMessages := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: parentGoal},
	}
	runtimeMessages := []model.Message{
		{Role: model.RoleSystem, Content: "runtime guidance"},
		{Role: model.RoleUser, Content: parentGoal},
		{Role: model.RoleAssistant, Content: "live continuation"},
	}

	merged, err := mergeContextRuntimeMessages(contextMessages, runtimeMessages)
	if err != nil {
		t.Fatalf("merge direct goal: %v", err)
	}
	want := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions\n\nruntime guidance"},
		{Role: model.RoleUser, Content: parentGoal},
		{Role: model.RoleAssistant, Content: "live continuation"},
	}
	assertRuntimeMessagesEqual(t, merged, want)
}

func TestMergeContextRuntimeMessagesNestedGoalKeepsParentContextAndChildGoal(t *testing.T) {
	t.Parallel()
	contextMessages := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions"},
		{Role: model.RoleUser, Content: "original conversation request"},
	}
	runtimeMessages := []model.Message{
		{Role: model.RoleSystem, Content: "execute this bounded step"},
		{Role: model.RoleUser, Content: "child plan step goal"},
	}

	merged, err := mergeContextRuntimeMessages(contextMessages, runtimeMessages)
	if err != nil {
		t.Fatalf("merge nested goal: %v", err)
	}
	want := []model.Message{
		{Role: model.RoleSystem, Content: "frozen instructions\n\nexecute this bounded step"},
		{Role: model.RoleUser, Content: "original conversation request"},
		{Role: model.RoleUser, Content: "child plan step goal"},
	}
	assertRuntimeMessagesEqual(t, merged, want)
}

func TestMergeContextRuntimeMessagesStillRejectsRuntimeWithoutUserGoal(t *testing.T) {
	t.Parallel()
	_, err := mergeContextRuntimeMessages(
		[]model.Message{{Role: model.RoleUser, Content: parentGoal}},
		[]model.Message{{Role: model.RoleSystem, Content: "guidance only"}},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("expected invalid request, got %v", err)
	}
}

func assertRuntimeMessagesEqual(t *testing.T, got, want []model.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index].Role != want[index].Role || got[index].Content != want[index].Content {
			t.Fatalf("message[%d] = %#v, want %#v", index, got[index], want[index])
		}
	}
}
