package domain

import "time"

type Step struct {
	StepID, RunID, ParentStepID        string
	PlanID                             string
	StepIndex, Attempt                 int
	NodeID, ActivationPath, LaneID     string
	ActivationIndex, CompletionOrder   int
	Kind, Title, Description, Status   string
	DependsOnJSON                      string
	ExpectedToolsJSON                  string
	ResourceRefsJSON                   string
	ApprovalRequired                   bool
	WaitingKind, WaitingID, ChildRunID string
	ResultSummary                      string
	InputJSON, OutputJSON, ErrorJSON   string
	StartedAt                          time.Time
	EndedAt                            *time.Time
	CreatedAt, UpdatedAt               time.Time
}
