package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
)

var (
	ErrInvalidDataset = errors.New("invalid evaluation dataset")
	ErrDatasetHash    = errors.New("evaluation dataset hash mismatch")
)

// Dimension defines one deterministic report dimension.
type Dimension struct {
	Name   string  `json:"name"`
	Weight float64 `json:"weight"`
}

// Case is one immutable evaluation example.
type Case struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Input    json.RawMessage   `json:"input"`
	Expected json.RawMessage   `json:"expected"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// DatasetDraft is the mutable input accepted by CompileDataset.
type DatasetDraft struct {
	ID            string      `json:"id"`
	Revision      int         `json:"revision"`
	Name          string      `json:"name"`
	PassThreshold float64     `json:"passThreshold"`
	Dimensions    []Dimension `json:"dimensions"`
	Cases         []Case      `json:"cases"`
}

// Dataset is one compiled immutable evaluation revision.
type Dataset struct {
	ID            string      `json:"id"`
	Revision      int         `json:"revision"`
	Name          string      `json:"name"`
	PassThreshold float64     `json:"passThreshold"`
	Dimensions    []Dimension `json:"dimensions"`
	Cases         []Case      `json:"cases"`
	Hash          string      `json:"hash"`
}

type datasetHashMaterial struct {
	ID            string      `json:"id"`
	Revision      int         `json:"revision"`
	Name          string      `json:"name"`
	PassThreshold float64     `json:"passThreshold"`
	Dimensions    []Dimension `json:"dimensions"`
	Cases         []Case      `json:"cases"`
}

// CompileDataset validates, canonicalizes and freezes one Dataset revision.
func CompileDataset(draft DatasetDraft) (Dataset, error) {
	normalized := normalizeDatasetDraft(draft)
	if err := validateDatasetDraft(normalized); err != nil {
		return Dataset{}, err
	}
	dataset := Dataset{
		ID: normalized.ID, Revision: normalized.Revision, Name: normalized.Name,
		PassThreshold: normalized.PassThreshold,
		Dimensions:    cloneDimensions(normalized.Dimensions), Cases: cloneCases(normalized.Cases),
	}
	hash, err := datasetHash(dataset)
	if err != nil {
		return Dataset{}, err
	}
	dataset.Hash = hash
	return dataset, nil
}

// ValidateDataset proves that a compiled Dataset still matches its frozen hash.
func ValidateDataset(dataset Dataset) error {
	draft := normalizeDatasetDraft(DatasetDraft{
		ID: dataset.ID, Revision: dataset.Revision, Name: dataset.Name,
		PassThreshold: dataset.PassThreshold, Dimensions: dataset.Dimensions, Cases: dataset.Cases,
	})
	if err := validateDatasetDraft(draft); err != nil {
		return err
	}
	hash, err := datasetHash(Dataset{
		ID: draft.ID, Revision: draft.Revision, Name: draft.Name,
		PassThreshold: draft.PassThreshold, Dimensions: draft.Dimensions, Cases: draft.Cases,
	})
	if err != nil {
		return err
	}
	if hash != strings.TrimSpace(dataset.Hash) {
		return ErrDatasetHash
	}
	return nil
}

func normalizeDatasetDraft(draft DatasetDraft) DatasetDraft {
	draft.ID = strings.TrimSpace(draft.ID)
	draft.Name = strings.TrimSpace(draft.Name)
	if draft.PassThreshold == 0 {
		draft.PassThreshold = 0.8
	}
	draft.Dimensions = cloneDimensions(draft.Dimensions)
	for index := range draft.Dimensions {
		draft.Dimensions[index].Name = strings.TrimSpace(draft.Dimensions[index].Name)
		if draft.Dimensions[index].Weight == 0 {
			draft.Dimensions[index].Weight = 1
		}
	}
	sort.Slice(draft.Dimensions, func(left int, right int) bool {
		return draft.Dimensions[left].Name < draft.Dimensions[right].Name
	})
	draft.Cases = cloneCases(draft.Cases)
	for index := range draft.Cases {
		draft.Cases[index] = normalizeCase(draft.Cases[index])
	}
	sort.Slice(draft.Cases, func(left int, right int) bool { return draft.Cases[left].ID < draft.Cases[right].ID })
	return draft
}

func normalizeCase(item Case) Case {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Input = normalizeRawJSON(item.Input, json.RawMessage(`null`))
	item.Expected = normalizeRawJSON(item.Expected, json.RawMessage(`null`))
	item.Metadata = normalizeMetadata(item.Metadata)
	return item
}

func validateDatasetDraft(draft DatasetDraft) error {
	if draft.ID == "" || draft.Revision <= 0 || draft.Name == "" ||
		draft.PassThreshold < 0 || draft.PassThreshold > 1 || len(draft.Dimensions) == 0 || len(draft.Cases) == 0 {
		return ErrInvalidDataset
	}
	if !uniqueDimensions(draft.Dimensions) || !uniqueCases(draft.Cases) {
		return ErrInvalidDataset
	}
	return nil
}

func uniqueDimensions(dimensions []Dimension) bool {
	seen := make(map[string]struct{}, len(dimensions))
	for _, dimension := range dimensions {
		if dimension.Name == "" || dimension.Weight <= 0 || math.IsNaN(dimension.Weight) || math.IsInf(dimension.Weight, 0) {
			return false
		}
		if _, duplicate := seen[dimension.Name]; duplicate {
			return false
		}
		seen[dimension.Name] = struct{}{}
	}
	return true
}

func uniqueCases(cases []Case) bool {
	seen := make(map[string]struct{}, len(cases))
	for _, item := range cases {
		if item.ID == "" || item.Name == "" || !json.Valid(item.Input) || !json.Valid(item.Expected) {
			return false
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}

func datasetHash(dataset Dataset) (string, error) {
	material := datasetHashMaterial{
		ID: dataset.ID, Revision: dataset.Revision, Name: dataset.Name,
		PassThreshold: dataset.PassThreshold,
		Dimensions:    cloneDimensions(dataset.Dimensions), Cases: cloneCases(dataset.Cases),
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", errors.Join(ErrInvalidDataset, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func cloneDataset(dataset Dataset) Dataset {
	dataset.Dimensions = cloneDimensions(dataset.Dimensions)
	dataset.Cases = cloneCases(dataset.Cases)
	return dataset
}

func cloneDimensions(values []Dimension) []Dimension {
	return append([]Dimension(nil), values...)
}

func cloneCases(values []Case) []Case {
	result := append([]Case(nil), values...)
	for index := range result {
		result[index].Input = cloneJSON(result[index].Input)
		result[index].Expected = cloneJSON(result[index].Expected)
		result[index].Metadata = cloneMetadata(result[index].Metadata)
	}
	return result
}

func normalizeMetadata(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		result[key] = strings.TrimSpace(value)
	}
	return result
}

func cloneMetadata(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func normalizeRawJSON(value json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		value = fallback
	}
	var normalized any
	if err := json.Unmarshal(value, &normalized); err != nil {
		return cloneJSON(value)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return cloneJSON(value)
	}
	return encoded
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
