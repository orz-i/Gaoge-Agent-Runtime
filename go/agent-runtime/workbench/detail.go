package workbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const CapabilityQuery kernel.Capability = "workbench.query"

var (
	ErrInvalidInput = errors.New("invalid workbench input")
	ErrUnavailable  = errors.New("workbench section unavailable")
)

// RunSource is the only required Workbench dependency.
type RunSource interface {
	Load(context.Context, string) (kernel.Snapshot, error)
}

// Provider contributes one optional named read-only Section and Timeline.
type Provider interface {
	Name() string
	Section(context.Context, kernel.Snapshot) (json.RawMessage, bool, error)
	Timeline(context.Context, kernel.Snapshot) ([]TimelineItem, error)
}

// Registration freezes one optional feature provider.
type Registration struct {
	Provider Provider
}

// Section is one canonical optional feature projection.
type Section struct {
	Name      string          `json:"name"`
	Available bool            `json:"available"`
	Content   json.RawMessage `json:"content,omitempty"`
	Hash      string          `json:"hash,omitempty"`
}

// Diagnostic records optional provider failure without breaking base detail.
type Diagnostic struct {
	Provider  string `json:"provider"`
	Operation string `json:"operation"`
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
}

// Overview is the compact feature-neutral Run summary.
type Overview struct {
	RunID         string           `json:"runID"`
	Kind          kernel.RunKind   `json:"kind"`
	Goal          string           `json:"goal"`
	Status        kernel.RunStatus `json:"status"`
	Revision      uint64           `json:"revision"`
	ErrorCode     string           `json:"errorCode,omitempty"`
	ErrorDetail   string           `json:"errorDetail,omitempty"`
	EventCount    int              `json:"eventCount"`
	HasCheckpoint bool             `json:"hasCheckpoint"`
	HasResult     bool             `json:"hasResult"`
}

// Detail is one deterministic read-only Workbench response.
type Detail struct {
	Overview    Overview           `json:"overview"`
	Run         kernel.Run         `json:"run"`
	Checkpoint  *kernel.Checkpoint `json:"checkpoint,omitempty"`
	Result      *kernel.Result     `json:"result,omitempty"`
	Sections    []Section          `json:"sections"`
	Timeline    []TimelineItem     `json:"timeline"`
	Diagnostics []Diagnostic       `json:"diagnostics,omitempty"`
	Hash        string             `json:"hash"`
}

// Query composes one immutable set of narrow read providers.
type Query struct {
	runs      RunSource
	providers []Provider
}

// NewQuery freezes provider order by name and rejects duplicates.
func NewQuery(runs RunSource, registrations []Registration) (*Query, error) {
	if runs == nil {
		return nil, ErrInvalidInput
	}
	providers := make([]Provider, 0, len(registrations))
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if registration.Provider == nil {
			return nil, ErrInvalidInput
		}
		name := strings.TrimSpace(registration.Provider.Name())
		if name == "" {
			return nil, ErrInvalidInput
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, ErrInvalidInput
		}
		seen[name] = struct{}{}
		providers = append(providers, registration.Provider)
	}
	sort.Slice(providers, func(left int, right int) bool {
		return strings.TrimSpace(providers[left].Name()) < strings.TrimSpace(providers[right].Name())
	})
	return &Query{runs: runs, providers: providers}, nil
}

// Descriptor declares the Workbench read capability.
func (query *Query) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "workbench", Provides: []kernel.Capability{CapabilityQuery}}
}

// Get returns base Run detail even when optional providers are unavailable.
func (query *Query) Get(ctx context.Context, runID string) (Detail, error) {
	runID = strings.TrimSpace(runID)
	if query == nil || query.runs == nil || runID == "" {
		return Detail{}, ErrInvalidInput
	}
	snapshot, err := query.runs.Load(ctx, runID)
	if err != nil {
		return Detail{}, err
	}
	sections, diagnostics := query.loadSections(ctx, snapshot)
	timeline, timelineDiagnostics := query.loadTimeline(ctx, snapshot)
	diagnostics = append(diagnostics, timelineDiagnostics...)
	sortDiagnostics(diagnostics)
	detail := Detail{
		Overview: overview(snapshot), Run: snapshot.Run,
		Checkpoint: cloneCheckpoint(snapshot.Checkpoint), Result: cloneResult(snapshot.Result),
		Sections: sections, Timeline: timeline, Diagnostics: diagnostics,
	}
	hash, err := detailHash(detail)
	if err != nil {
		return Detail{}, err
	}
	detail.Hash = hash
	return cloneDetail(detail), nil
}

func (query *Query) loadSections(ctx context.Context, snapshot kernel.Snapshot) ([]Section, []Diagnostic) {
	sections := make([]Section, 0, len(query.providers))
	diagnostics := make([]Diagnostic, 0)
	for _, provider := range query.providers {
		name := strings.TrimSpace(provider.Name())
		content, available, err := provider.Section(ctx, cloneSnapshot(snapshot))
		section, diagnostic := normalizeSection(name, content, available, err)
		sections = append(sections, section)
		if diagnostic != nil {
			diagnostics = append(diagnostics, *diagnostic)
		}
	}
	return sections, diagnostics
}

func normalizeSection(name string, content json.RawMessage, available bool, sectionErr error) (Section, *Diagnostic) {
	section := Section{Name: name, Available: available}
	if sectionErr != nil {
		section.Available = false
		code := "provider_error"
		if errors.Is(sectionErr, ErrUnavailable) {
			code = "unavailable"
		}
		return section, &Diagnostic{
			Provider: name, Operation: "section", Code: code, Message: truncate(sectionErr.Error(), 512),
		}
	}
	if !available {
		return section, nil
	}
	canonical, err := canonicalJSON(content)
	if err != nil {
		section.Available = false
		return section, &Diagnostic{
			Provider: name, Operation: "section", Code: "invalid_content", Message: err.Error(),
		}
	}
	section.Content = canonical
	section.Hash = hashBytes(canonical)
	return section, nil
}

func overview(snapshot kernel.Snapshot) Overview {
	return Overview{
		RunID: snapshot.Run.ID, Kind: snapshot.Run.Kind, Goal: snapshot.Run.Goal,
		Status: snapshot.Run.Status, Revision: snapshot.Run.Revision,
		ErrorCode: snapshot.Run.ErrorCode, ErrorDetail: snapshot.Run.ErrorDetail,
		EventCount: len(snapshot.Events), HasCheckpoint: snapshot.Checkpoint != nil, HasResult: snapshot.Result != nil,
	}
}

func detailHash(detail Detail) (string, error) {
	material := detail
	material.Hash = ""
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", errors.Join(ErrInvalidInput, err)
	}
	return hashBytes(encoded), nil
}

func canonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	if !json.Valid(value) {
		return nil, ErrInvalidInput
	}
	var normalized any
	if err := json.Unmarshal(value, &normalized); err != nil {
		return nil, ErrInvalidInput
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, ErrInvalidInput
	}
	return encoded, nil
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func sortDiagnostics(values []Diagnostic) {
	sort.Slice(values, func(left int, right int) bool {
		if values[left].Provider != values[right].Provider {
			return values[left].Provider < values[right].Provider
		}
		if values[left].Operation != values[right].Operation {
			return values[left].Operation < values[right].Operation
		}
		return values[left].Code < values[right].Code
	})
}

func cloneDetail(detail Detail) Detail {
	detail.Checkpoint = cloneCheckpoint(detail.Checkpoint)
	detail.Result = cloneResult(detail.Result)
	detail.Sections = append([]Section(nil), detail.Sections...)
	for index := range detail.Sections {
		detail.Sections[index].Content = append(json.RawMessage(nil), detail.Sections[index].Content...)
	}
	detail.Timeline = cloneTimeline(detail.Timeline)
	detail.Diagnostics = append([]Diagnostic(nil), detail.Diagnostics...)
	return detail
}

func cloneSnapshot(snapshot kernel.Snapshot) kernel.Snapshot {
	snapshot.State = append(json.RawMessage(nil), snapshot.State...)
	snapshot.Checkpoint = cloneCheckpoint(snapshot.Checkpoint)
	snapshot.Result = cloneResult(snapshot.Result)
	snapshot.Events = append([]kernel.Event(nil), snapshot.Events...)
	for index := range snapshot.Events {
		snapshot.Events[index].Data = append(json.RawMessage(nil), snapshot.Events[index].Data...)
	}
	return snapshot
}

func cloneCheckpoint(checkpoint *kernel.Checkpoint) *kernel.Checkpoint {
	if checkpoint == nil {
		return nil
	}
	result := *checkpoint
	result.Payload = append(json.RawMessage(nil), checkpoint.Payload...)
	result.Response = append(json.RawMessage(nil), checkpoint.Response...)
	if checkpoint.ResolvedAt != nil {
		resolvedAt := *checkpoint.ResolvedAt
		result.ResolvedAt = &resolvedAt
	}
	return &result
}

func cloneResult(result *kernel.Result) *kernel.Result {
	if result == nil {
		return nil
	}
	cloned := *result
	cloned.Content = append(json.RawMessage(nil), result.Content...)
	return &cloned
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}
