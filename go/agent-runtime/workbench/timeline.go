package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	timelineOperation = "timeline"
	kernelSource      = "kernel"
)

// TimelineItem is one read-only observation ordered across Kernel and feature providers.
type TimelineItem struct {
	ID        string          `json:"id"`
	Source    string          `json:"source"`
	Kind      string          `json:"kind"`
	Status    string          `json:"status,omitempty"`
	Title     string          `json:"title,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
	Data      json.RawMessage `json:"data,omitempty"`
}

func (query *Query) loadTimeline(
	ctx context.Context,
	snapshot kernel.Snapshot,
	events []kernel.Event,
) ([]TimelineItem, []Diagnostic) {
	items := baseTimeline(snapshot, events)
	diagnostics := make([]Diagnostic, 0)
	for _, provider := range query.providers {
		name := strings.TrimSpace(provider.Name())
		provided, err := provider.Timeline(ctx, cloneSnapshot(snapshot))
		if err != nil {
			code := "provider_error"
			if errors.Is(err, ErrUnavailable) {
				code = "unavailable"
			}
			diagnostics = append(diagnostics, Diagnostic{
				Provider: name, Operation: timelineOperation, Code: code, Message: truncate(err.Error(), 512),
			})
			continue
		}
		for index := range provided {
			provided[index].Source = name
		}
		items = append(items, provided...)
	}
	normalized, mergeDiagnostics := normalizeTimeline(items)
	diagnostics = append(diagnostics, mergeDiagnostics...)
	return normalized, diagnostics
}

func baseTimeline(snapshot kernel.Snapshot, events []kernel.Event) []TimelineItem {
	items := make([]TimelineItem, 0, len(events)+2)
	for _, event := range events {
		items = append(items, TimelineItem{
			ID: fmt.Sprintf("event:%d", event.Seq), Source: kernelSource, Kind: event.Type,
			Title: event.Message, Seq: event.Seq, CreatedAt: event.CreatedAt,
			Data: append(json.RawMessage(nil), event.Data...),
		})
	}
	if snapshot.Checkpoint != nil {
		checkpoint := snapshot.Checkpoint
		items = append(items, TimelineItem{
			ID: "checkpoint:" + checkpoint.ID, Source: kernelSource, Kind: "checkpoint." + checkpoint.Kind,
			Status: string(checkpoint.Status), Title: checkpoint.Kind, CreatedAt: checkpoint.CreatedAt,
			Data: checkpointTimelineData(checkpoint),
		})
	}
	if snapshot.Result != nil {
		items = append(items, TimelineItem{
			ID: "result", Source: kernelSource, Kind: "run.result", Status: string(snapshot.Run.Status),
			Title: snapshot.Result.ContentType, CreatedAt: snapshot.Run.UpdatedAt,
			Data: append(json.RawMessage(nil), snapshot.Result.Content...),
		})
	}
	return items
}

func checkpointTimelineData(checkpoint *kernel.Checkpoint) json.RawMessage {
	value := struct {
		Payload    json.RawMessage `json:"payload"`
		Response   json.RawMessage `json:"response,omitempty"`
		ResolvedAt *time.Time      `json:"resolvedAt,omitempty"`
	}{
		Payload:    append(json.RawMessage(nil), checkpoint.Payload...),
		Response:   append(json.RawMessage(nil), checkpoint.Response...),
		ResolvedAt: checkpoint.ResolvedAt,
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

func normalizeTimeline(items []TimelineItem) ([]TimelineItem, []Diagnostic) {
	byKey := make(map[string]TimelineItem, len(items))
	diagnostics := make([]Diagnostic, 0)
	for _, item := range items {
		normalized, err := normalizeTimelineItem(item)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Provider: strings.TrimSpace(item.Source), Operation: timelineOperation,
				Code: "invalid_item", Message: err.Error(),
			})
			continue
		}
		key := normalized.Source + "\x1f" + normalized.ID
		if previous, duplicate := byKey[key]; duplicate {
			if timelineItemHash(previous) != timelineItemHash(normalized) {
				diagnostics = append(diagnostics, Diagnostic{
					Provider: normalized.Source, Operation: timelineOperation,
					Code: "identity_conflict", Message: normalized.ID,
				})
			}
			continue
		}
		byKey[key] = normalized
	}
	result := make([]TimelineItem, 0, len(byKey))
	for _, item := range byKey {
		result = append(result, item)
	}
	sort.Slice(result, func(left int, right int) bool {
		return timelineLess(result[left], result[right])
	})
	return result, diagnostics
}

func normalizeTimelineItem(item TimelineItem) (TimelineItem, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Source = strings.TrimSpace(item.Source)
	item.Kind = strings.TrimSpace(item.Kind)
	item.Status = strings.TrimSpace(item.Status)
	item.Title = truncate(item.Title, 240)
	item.Summary = truncate(item.Summary, 1_024)
	if item.ID == "" || item.Source == "" || item.Kind == "" || item.CreatedAt.IsZero() || item.Seq < 0 {
		return TimelineItem{}, ErrInvalidInput
	}
	if len(item.Data) > 0 {
		canonical, err := canonicalJSON(item.Data)
		if err != nil {
			return TimelineItem{}, err
		}
		item.Data = canonical
	}
	return item, nil
}

func timelineLess(left TimelineItem, right TimelineItem) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	if left.Source != right.Source {
		return left.Source < right.Source
	}
	return left.ID < right.ID
}

func timelineItemHash(item TimelineItem) string {
	encoded, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	return hashBytes(encoded)
}

func cloneTimeline(values []TimelineItem) []TimelineItem {
	result := append([]TimelineItem(nil), values...)
	for index := range result {
		result[index].Data = append(json.RawMessage(nil), result[index].Data...)
	}
	return result
}
