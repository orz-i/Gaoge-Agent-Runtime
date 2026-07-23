package domain

import "time"

type Evidence struct {
	EvidenceID, SourceKind, SourceID string
	Actor                            ActorRef
	Projection                       ProjectionRef
	Kind, SelectorJSON, Title        string
	Excerpt, ContentHash             string
	SourceContentHash                string
	CreatedAt, UpdatedAt             time.Time
}
