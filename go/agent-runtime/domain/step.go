package domain

import "time"

type Step struct {
	StepID, RunID, ParentStepID      string
	PlanID                           string
	StepIndex, Attempt               int
	Kind, Title, Description, Status string
	DependsOnJSON                    string
	ExpectedToolsJSON                string
	ResourceRefsJSON                 string
	ApprovalRequired                 bool
	ResultSummary                    string
	InputJSON, OutputJSON, ErrorJSON string
	StartedAt                        time.Time
	EndedAt                          *time.Time
	CreatedAt, UpdatedAt             time.Time
}
