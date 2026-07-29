package agentruntime

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type workflowDefinitionLookupCaptureStore struct {
	Store
	ref model.ResourceRef
}

func (s *workflowDefinitionLookupCaptureStore) GetWorkflowDefinition(
	_ context.Context,
	_ model.ActorRef,
	ref model.ResourceRef,
) (*model.WorkflowDefinition, error) {
	s.ref = ref
	return &model.WorkflowDefinition{WorkflowID: ref.ID}, nil
}

func TestNormalizeWorkflowIDPreservesCanonicalIDsWithinPersistenceLimit(t *testing.T) {
	t.Parallel()

	const canonical = "workflow_story_execution_prepare_develop_full_outline_v1_actor_1"
	if got := normalizeWorkflowID(canonical); got != canonical {
		t.Fatalf("normalizeWorkflowID() = %q, want %q", got, canonical)
	}
}

func TestNormalizeWorkflowIDBoundsLongIDsDeterministically(t *testing.T) {
	t.Parallel()

	const input = "story_execution_prepare_develop_full_outline_v1_actor_56"
	first := normalizeWorkflowID(input)
	second := normalizeWorkflowID(input)
	if first != second {
		t.Fatalf("normalizeWorkflowID() is not deterministic: %q != %q", first, second)
	}
	if got := utf8.RuneCountInString(first); got != maxWorkflowDefinitionIDRunes {
		t.Fatalf("normalized ID length = %d, want %d: %q", got, maxWorkflowDefinitionIDRunes, first)
	}
	if !strings.HasPrefix(first, "workflow_story_execution_prepare_develop_full_") {
		t.Fatalf("normalized ID lost its observable prefix: %q", first)
	}
	if first == normalizeWorkflowID("story_execution_prepare_develop_full_outline_v1_actor_57") {
		t.Fatal("distinct long workflow IDs normalized to the same value")
	}
}

func TestNormalizeWorkflowIDBoundsUnicodeByCharacters(t *testing.T) {
	t.Parallel()

	input := strings.Repeat("工作流", 30)
	got := normalizeWorkflowID(input)
	if count := utf8.RuneCountInString(got); count != maxWorkflowDefinitionIDRunes {
		t.Fatalf("normalized unicode ID length = %d, want %d", count, maxWorkflowDefinitionIDRunes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("normalized unicode ID is invalid UTF-8: %q", got)
	}
}

func TestGetWorkflowDefinitionUsesCanonicalBoundedID(t *testing.T) {
	t.Parallel()

	store := &workflowDefinitionLookupCaptureStore{}
	engine := &Engine{repo: store}
	const input = "story_execution_prepare_develop_full_outline_v1_actor_56"
	item, err := engine.GetWorkflowDefinition(
		context.Background(),
		model.ActorRef{TenantID: valueDefault572954E1, ActorID: "56"},
		model.ResourceRef{Kind: model.WorkflowDefinitionKind, ID: input},
	)
	if err != nil {
		t.Fatalf("GetWorkflowDefinition() error = %v", err)
	}
	want := normalizeWorkflowID(input)
	if store.ref.ID != want || item.WorkflowID != want {
		t.Fatalf("lookup ID = %q, item ID = %q, want %q", store.ref.ID, item.WorkflowID, want)
	}
}
