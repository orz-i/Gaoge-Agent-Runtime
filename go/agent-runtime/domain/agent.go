package domain

import (
	"strconv"
	"time"
)

const (
	AgentManifestKind           = "agent_manifest"
	AgentManifestStatusActive   = "active"
	AgentManifestStatusDisabled = "disabled"

	RunHandoffStatusQueued    = "queued"
	RunHandoffStatusCompleted = "completed"
	RunHandoffStatusFailed    = "failed"
	RunHandoffStatusCancelled = "cancelled"
)

// AgentManifest is one immutable revision of a tenant-visible Agent contract.
// Environment policies remain the authorization ceiling; a manifest may only
// narrow the model, execution mode, tools, and skills used by a delegated Run.
type AgentManifest struct {
	ManifestID         string
	Revision           int
	TenantID           string
	Name               string
	Description        string
	Instructions       string
	Status             string
	ModelName          string
	ExecutionMode      string
	ToolKeys           []string
	SkillRefs          []ResourceRef
	MaxChildRuns       int
	MaxDepth           int
	CreatedBy          ActorRef
	RequestID          string
	RequestFingerprint string
	RevisionNote       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func (m AgentManifest) Ref() ResourceRef {
	return ResourceRef{Kind: AgentManifestKind, ID: m.ManifestID, Revision: manifestRevisionString(m.Revision)}
}

type AgentManifestFilter struct {
	Status string
	Limit  int
	Offset int
}

type AgentManifestPage struct {
	Total   int64
	Results []AgentManifest
}

// RunHandoff is the durable delegation boundary between one parent Run and one
// child Run. ClientHandoffID plus RequestFingerprint is the replay boundary.
type RunHandoff struct {
	HandoffID          string
	ClientHandoffID    string
	RequestFingerprint string
	Actor              ActorRef
	RootRunID          string
	ParentRunID        string
	ChildRunID         string
	AgentManifest      ResourceRef
	AgentName          string
	Goal               string
	Status             string
	Depth              int
	InputProjection    ProjectionRef
	ResultSummary      string
	ResultOutputIDs    []string
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type RunHandoffFilter struct {
	RootRunID   string
	ParentRunID string
	ChildRunID  string
	Status      string
	Limit       int
	Offset      int
}

type RunHandoffPage struct {
	Total   int64
	Results []RunHandoff
}

type RunHandoffCompletion struct {
	Status          string
	ResultSummary   string
	ResultOutputIDs []string
	ErrorCode       string
	ErrorMessage    string
	CompletedAt     time.Time
}

func manifestRevisionString(revision int) string {
	if revision <= 0 {
		return ""
	}
	return strconv.Itoa(revision)
}
