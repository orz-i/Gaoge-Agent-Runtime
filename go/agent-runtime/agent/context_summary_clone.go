package agent

import (
	"slices"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Isolated, byte-preserving copies at native Context Summary Host boundaries.
// A copied Provider replay or Cursor is never a new execution authority.
func cloneSummaryCursor(v SummaryCursor) SummaryCursor {
	v.Checkpoint = cloneSummaryCheckpoint(v.Checkpoint)
	return v
}
func cloneSummarySnapshot(v kernel.Snapshot) kernel.Snapshot {
	if v.Run.DeadlineAt != nil {
		t := *v.Run.DeadlineAt
		v.Run.DeadlineAt = &t
	}
	if v.Run.EndedAt != nil {
		t := *v.Run.EndedAt
		v.Run.EndedAt = &t
	}
	v.State = slices.Clone(v.State)
	if v.Checkpoint != nil {
		c := *v.Checkpoint
		c.Payload = slices.Clone(c.Payload)
		c.Response = slices.Clone(c.Response)
		if c.ResolvedAt != nil {
			t := *c.ResolvedAt
			c.ResolvedAt = &t
		}
		v.Checkpoint = &c
	}
	if v.Result != nil {
		r := *v.Result
		r.Content = slices.Clone(r.Content)
		v.Result = &r
	}
	return v
}
func cloneSummaryCheckpoint(v runtimecontext.Checkpoint) runtimecontext.Checkpoint {
	if state := v.Window.WorkingState; state != nil {
		copy := *state
		copy.Constraints = slices.Clone(state.Constraints)
		copy.Completed = slices.Clone(state.Completed)
		copy.Pending = slices.Clone(state.Pending)
		copy.Decisions = slices.Clone(state.Decisions)
		copy.EvidenceRefs = slices.Clone(state.EvidenceRefs)
		if state.Derivation != nil {
			derivation := *state.Derivation
			copy.Derivation = &derivation
		}
		v.Window.WorkingState = &copy
	}
	v.Window.Entries = slices.Clone(v.Window.Entries)
	for i := range v.Window.Entries {
		v.Window.Entries[i].Message = cloneSummaryMessage(v.Window.Entries[i].Message)
	}
	v.ArtifactIDs = slices.Clone(v.ArtifactIDs)
	if v.Trace.LastAssessment != nil {
		a := *v.Trace.LastAssessment
		v.Trace.LastAssessment = &a
	}
	return v
}
func cloneSummaryRequest(v model.Request) model.Request {
	v.ModelOptions = slices.Clone(v.ModelOptions)
	if v.HostedToolCallLimit != nil {
		n := *v.HostedToolCallLimit
		v.HostedToolCallLimit = &n
	}
	if v.OutputContract != nil {
		c := *v.OutputContract
		c.Schema = slices.Clone(c.Schema)
		v.OutputContract = &c
	}
	v.Messages = cloneSummaryMessages(v.Messages)
	v.Tools = slices.Clone(v.Tools)
	for i := range v.Tools {
		v.Tools[i].InputSchema = slices.Clone(v.Tools[i].InputSchema)
	}
	v.HostedTools = slices.Clone(v.HostedTools)
	for i := range v.HostedTools {
		v.HostedTools[i].Target = slices.Clone(v.HostedTools[i].Target)
	}
	return v
}
func cloneSummaryMessages(v []model.Message) []model.Message {
	v = slices.Clone(v)
	for i := range v {
		v[i] = cloneSummaryMessage(v[i])
	}
	return v
}
func cloneSummaryMessage(v model.Message) model.Message {
	v.CacheControl = model.CloneCacheControl(v.CacheControl)
	v.Content = model.CloneContentParts(v.Content)
	if v.ToolResult != nil {
		r := *v.ToolResult
		v.ToolResult = &r
	}
	v.ToolCalls = slices.Clone(v.ToolCalls)
	for i := range v.ToolCalls {
		v.ToolCalls[i].Arguments = slices.Clone(v.ToolCalls[i].Arguments)
	}
	if v.ProviderReplay != nil {
		r := *v.ProviderReplay
		r.Data = slices.Clone(r.Data)
		v.ProviderReplay = &r
	}
	return v
}
