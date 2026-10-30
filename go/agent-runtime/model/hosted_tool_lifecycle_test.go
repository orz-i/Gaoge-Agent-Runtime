package model_test

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedToolTerminalStatusIsConservativeAndCaseInsensitive(t *testing.T) {
	for _, tc := range []struct {
		status   string
		terminal bool
	}{
		{"", false},
		{" completed ", true},
		{"    ", false},
		{"FAILED", true},
		{"error", true},
		{"cancelled", true},
		{"canceled", true},
		{"requested", false},
		{" pending ", false},
		{"In_Progress", false},
		{"running", false},
		{"queued", false},
		{"provider_unrecognized", false},
	} {
		if got := model.HostedToolTerminalStatus(tc.status); got != tc.terminal {
			t.Errorf("status %q terminal=%v want %v", tc.status, got, tc.terminal)
		}
	}
}

func TestCollapseHostedToolCallsPrefersTerminalIndependentOfOrder(t *testing.T) {
	for _, cases := range []struct {
		name          string
		first, second model.HostedToolCall
		wantStatus    string
	}{
		{
			name:       "pending then completed",
			first:      model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "in_progress"},
			second:     model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "completed", Output: []byte(`{"ok":true}`)},
			wantStatus: "completed",
		},
		{
			name:       "completed then pending",
			first:      model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "completed", Output: []byte(`{"ok":true}`)},
			second:     model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "in_progress"},
			wantStatus: "completed",
		},
		{
			name:       "running then failed",
			first:      model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "running"},
			second:     model.HostedToolCall{ID: "search", ToolKey: "native.search", Status: "failed"},
			wantStatus: "failed",
		},
	} {
		t.Run(cases.name, func(t *testing.T) {
			calls := []model.HostedToolCall{
				cases.first, cases.second,
				{ID: "search", ToolKey: "native.image", Status: "completed"},
				{ToolKey: "native.search", Status: "completed"},
				{ToolKey: "native.search", Status: "completed"},
			}
			merged := model.CollapseHostedToolCalls(calls)
			if len(merged) != 4 || merged[0].Status != cases.wantStatus ||
				merged[1].ToolKey != "native.image" || merged[2].ID != "" || merged[3].ID != "" {
				t.Fatalf("incorrect identity/terminal coalescing: %+v", merged)
			}
			if calls[0].Status != cases.first.Status || calls[1].Status != cases.second.Status {
				t.Fatalf("mutated original call evidence: %+v", calls)
			}
		})
	}
}

func TestCollapseHostedToolCallsDoesNotDowngradeConflictingTerminals(t *testing.T) {
	merged := model.CollapseHostedToolCalls([]model.HostedToolCall{
		{ID: "same", ToolKey: "native.search", Status: "failed"},
		{ID: "same", ToolKey: "native.search", Status: "in_progress"},
		{ID: "same", ToolKey: "native.search", Status: "completed"},
	})
	if len(merged) != 1 || merged[0].Status != "failed" {
		t.Fatalf("prior authoritative terminal downgraded: %+v", merged)
	}
}
