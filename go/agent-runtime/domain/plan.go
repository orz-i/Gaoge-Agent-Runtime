package domain

import "time"

const (
	PlanDraft      = "draft"
	PlanProposed   = "proposed"
	PlanApproved   = "approved"
	PlanRejected   = "rejected"
	PlanSuperseded = "superseded"
)

type Plan struct {
	PlanID, RunID string
	Revision      int
	Status        string
	Goal          string
	Summary       string
	PayloadJSON   string
	CreatedAt     time.Time
	ApprovedAt    *time.Time
	UpdatedAt     time.Time
}
