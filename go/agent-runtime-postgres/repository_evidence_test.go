package postgres

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestEvidenceIsActorScopedAndKeepsImmutableExcerpt(t *testing.T) {
	db := openConversationRepositoryTestDB(t)
	if err := db.AutoMigrate(&models.EvidenceSelection{}); err != nil {
		t.Fatal(err)
	}
	repo := New(db, StaticSessions(db))
	ctx := context.Background()
	owner := domain.ActorRef{TenantID: "tenant_a", ActorID: "actor_21"}
	evidence := &domain.Evidence{
		EvidenceID:        "evidence_one",
		SourceKind:        "output",
		SourceID:          "output_33",
		Actor:             owner,
		Projection:        domain.ProjectionRef{Kind: projectionKindMessage, ID: "message_44"},
		Kind:              "text_range",
		SelectorJSON:      `{"start":0,"end":4}`,
		Title:             "Excerpt",
		Excerpt:           "fixed content",
		ContentHash:       "hash",
		SourceContentHash: "source-hash",
	}
	if err := repo.CreateEvidence(ctx, evidence); err != nil {
		t.Fatal(err)
	}
	owned, err := repo.GetEvidenceByIDs(ctx, owner, []string{evidence.EvidenceID})
	if err != nil || len(owned) != 1 || owned[0].SourceID != "output_33" || owned[0].Excerpt != "fixed content" || owned[0].Projection.ID != "message_44" {
		t.Fatalf("owned evidence = %#v, err=%v", owned, err)
	}
	foreign, err := repo.GetEvidenceByIDs(ctx, domain.ActorRef{TenantID: "tenant_a", ActorID: "actor_22"}, []string{evidence.EvidenceID})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign evidence leaked: %#v, err=%v", foreign, err)
	}
}
