package main

import (
	"bytes"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// probeSchemaCheck contains only fixed types, booleans and counts. Unknown
// object/field facts are omitted rather than reported as false. It never holds
// response content, property names, model values or validation error details.
type probeSchemaCheck struct {
	ContentPresent      bool   `json:"contentPresent"`
	JSONValid           bool   `json:"jsonValid"`
	JSONType            string `json:"jsonType"`
	SchemaValid         bool   `json:"schemaValid"`
	OKPresent           *bool  `json:"okPresent,omitempty"`
	OKType              string `json:"okType,omitempty"`
	OKIsTrue            *bool  `json:"okIsTrue,omitempty"`
	ExtraPropertyCount  *int   `json:"extraPropertyCount,omitempty"`
	ToolCallCount       int    `json:"toolCallCount"`
	HostedToolCallCount int    `json:"hostedToolCallCount"`
	ArtifactCount       int    `json:"artifactCount"`
	Continue            bool   `json:"continue"`
}

// diagnoseProbeSchema explains the fixed probe without changing its validator's
// decision. Parsing here never repairs, extracts or accepts an invalid answer.
func diagnoseProbeSchema(response model.Response, schemaValid bool) *probeSchemaCheck {
	check := &probeSchemaCheck{
		ContentPresent: len(response.Content) != 0, JSONType: "invalid", SchemaValid: schemaValid,
		ToolCallCount: len(response.ToolCalls), HostedToolCallCount: len(response.HostedToolCalls),
		ArtifactCount: len(response.Artifacts), Continue: response.Continue,
	}
	raw := []byte(response.Content)
	if !json.Valid(raw) {
		return check
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return check
	}
	check.JSONValid, check.JSONType = true, probeJSONType(value)
	object, ok := value.(map[string]any)
	if !ok {
		return check
	}
	value, present := object["ok"]
	check.OKPresent, check.OKType = &present, "missing"
	extra := len(object)
	if present {
		extra--
		check.OKType = probeJSONType(value)
		if boolean, isBoolean := value.(bool); isBoolean {
			check.OKIsTrue = &boolean
		}
	}
	check.ExtraPropertyCount = &extra
	return check
}

func probeJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "invalid"
	}
}
