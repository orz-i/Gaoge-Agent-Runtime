package agentruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const projectionEvidenceTestSourceHash = "source-hash"

func TestProjectionEvidenceExcerptSupportsFullAndRuneRange(t *testing.T) {
	content := ProjectionContent{ContentType: "text/markdown", Content: "甲乙 story", ContentHash: projectionEvidenceTestSourceHash}

	full, selector, ok := projectionEvidenceExcerpt(CreateEvidenceInput{Kind: valueFull}, content)
	if !ok || full != content.Content || selector[valueKind55B00946] != valueFull {
		t.Fatalf("full projection evidence mismatch: excerpt=%q selector=%v ok=%v", full, selector, ok)
	}

	ranged, selector, ok := projectionEvidenceExcerpt(CreateEvidenceInput{Kind: valueTextRange, Start: 1, End: 4}, content)
	if !ok || ranged != "乙 s" || selector["start"] != 1 || selector["end"] != 4 {
		t.Fatalf("range projection evidence mismatch: excerpt=%q selector=%v ok=%v", ranged, selector, ok)
	}
}

func TestProjectionEvidenceExcerptRejectsInvalidSelections(t *testing.T) {
	tests := []CreateEvidenceInput{
		{Kind: valueTextRange, Start: -1, End: 1},
		{Kind: valueTextRange, Start: 1, End: 99},
		{Kind: "table_range"},
	}
	for _, input := range tests {
		if _, _, ok := projectionEvidenceExcerpt(input, ProjectionContent{ContentType: valueText6CED98CE, Content: "body", ContentHash: projectionEvidenceTestSourceHash}); ok {
			t.Fatalf("selection should be rejected: %+v", input)
		}
	}
	if _, _, ok := projectionEvidenceExcerpt(CreateEvidenceInput{Kind: valueFull}, ProjectionContent{ContentType: "image/png", Content: "body", ContentHash: projectionEvidenceTestSourceHash}); ok {
		t.Fatal("non-text projection should be rejected")
	}
}

func TestOutputEvidenceExcerptSupportsTextAndTableRanges(t *testing.T) {
	text, _, ok := evidenceExcerpt(CreateEvidenceInput{Start: 1, End: 4}, &OutputPreview{Type: valueText6CED98CE, Content: "abcdef"}, valueTextRange)
	if !ok || text != "bcd" {
		t.Fatalf("text evidence = %q, ok=%v", text, ok)
	}
	table, _, ok := evidenceExcerpt(CreateEvidenceInput{RowStart: 0, RowEnd: 2, ColumnStart: 1, ColumnEnd: 3}, &OutputPreview{Type: valueTableF71860DA, Rows: [][]string{{"a", "b", "c"}, {"d", "e", "f"}}}, "table_range")
	if !ok || table != "b,c\ne,f\n" {
		t.Fatalf("table evidence = %q, ok=%v", table, ok)
	}
}

type projectionEvidenceRepository struct {
	Store
	items []domain.Evidence
}

func (r *projectionEvidenceRepository) CreateEvidence(_ context.Context, item *domain.Evidence) error {
	r.items = append(r.items, *item)
	return nil
}

func (r *projectionEvidenceRepository) GetEvidenceByIDs(_ context.Context, _ domain.ActorRef, _ []string) ([]domain.Evidence, error) {
	return append([]domain.Evidence(nil), r.items...), nil
}

type projectionContentStub struct {
	content ProjectionContent
	err     error
}

func (s *projectionContentStub) ResolveProjectionContent(context.Context, ResolveProjectionContentRequest) (ProjectionContent, error) {
	return s.content, s.err
}

func TestProjectionEvidenceFreezesContentAndRejectsEditedSource(t *testing.T) {
	repo := &projectionEvidenceRepository{}
	source := &projectionContentStub{content: ProjectionContent{Title: "Reply", ContentType: valueText6CED98CE, Content: "frozen reply", ContentHash: "hash-v1"}}
	service := &Engine{cfg: StaticConfigProvider(Config{}), repo: repo, projectionContent: source}
	actor := domain.ActorRef{TenantID: "tenant-1", ActorID: "actor-1"}
	thread := domain.ThreadRef{Kind: "conversation", ID: "thread-1"}
	projection := domain.ProjectionRef{Kind: "conversation.message", ID: "message-1"}

	evidence, err := service.CreateEvidence(t.Context(), CreateEvidenceInput{Actor: actor, Thread: thread, Projection: projection, SourceKind: valueProjectionSource, Kind: valueFull})
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Excerpt != "frozen reply" || evidence.SourceContentHash != "hash-v1" || evidence.Projection != projection || evidence.SourceID != projection.ID {
		t.Fatalf("unexpected frozen evidence: %+v", evidence)
	}

	source.content.Content, source.content.ContentHash = "edited reply", "hash-v2"
	_, err = service.resolveTextRunEvidenceRefs(t.Context(), actor, thread, []string{evidence.EvidenceID})
	if !errors.Is(err, ErrWorkspaceSourceStale) {
		t.Fatalf("edited projection error = %v, want %v", err, ErrWorkspaceSourceStale)
	}
}

func TestProjectionEvidenceRejectsUnresolvableProjection(t *testing.T) {
	service := &Engine{cfg: StaticConfigProvider(Config{}), repo: &projectionEvidenceRepository{}, projectionContent: &projectionContentStub{err: ErrThreadNotFound}}
	_, err := service.CreateEvidence(t.Context(), CreateEvidenceInput{
		Actor: domain.ActorRef{TenantID: "tenant-1", ActorID: "actor-2"}, Thread: domain.ThreadRef{Kind: "conversation", ID: "foreign-thread"},
		Projection: domain.ProjectionRef{Kind: "conversation.message", ID: "foreign-message"}, SourceKind: valueProjectionSource, Kind: valueFull,
	})
	if !errors.Is(err, ErrThreadNotFound) {
		t.Fatalf("unresolvable projection error = %v", err)
	}
}
