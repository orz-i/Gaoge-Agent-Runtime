package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

const (
	maxPreflightWireBytes    = 256 << 10
	maxPreflightWireMessages = 16
)

type wireCaptureState string

const (
	wireComplete        wireCaptureState = "complete"
	wireUnavailable     wireCaptureState = "unavailable"
	wireTooLarge        wireCaptureState = "too_large"
	wireIncomplete      wireCaptureState = "incomplete"
	wireReadError       wireCaptureState = "read_error"
	wireInvalidJSON     wireCaptureState = "invalid_json"
	wireInvalidEnvelope wireCaptureState = "invalid_envelope"
)

// All durable fields below are fixed labels, booleans or bounded counts.
// Wire facts never contain captured bytes. Failed-probe responseDebug is a
// separate, bounded and redacted view of those bytes.
type probeWireCheck struct {
	Request  probeWireRequestCheck  `json:"request"`
	Response probeWireResponseCheck `json:"response"`
}

type probeWireFormat struct {
	Type               string `json:"type"`
	StrictEnabled      bool   `json:"strictEnabled"`
	SchemaMatchesProbe bool   `json:"schemaMatchesProbe"`
}

type probeWireRequestCheck struct {
	Capture wireCaptureState `json:"capture"`
	Format  *probeWireFormat `json:"format,omitempty"`
}

type probeWireResponseCheck struct {
	Capture wireCaptureState `json:"capture"`
	*probeWireResponseFacts
}

type probeWireResponseFacts struct {
	Status            string             `json:"status"`
	Format            probeWireFormat    `json:"format"`
	OutputItemCount   int                `json:"outputItemCount"`
	MessageCount      int                `json:"messageCount"`
	MessagesTruncated bool               `json:"messagesTruncated"`
	Messages          []probeWireMessage `json:"messages"`
}

type probeWireMessage struct {
	Index                int    `json:"index"`
	Phase                string `json:"phase"`
	TextPartCount        int    `json:"textPartCount"`
	RefusalPartCount     int    `json:"refusalPartCount"`
	UnknownPartCount     int    `json:"unknownPartCount"`
	JSONValidPartCount   int    `json:"jsonValidPartCount"`
	SchemaValidPartCount int    `json:"schemaValidPartCount"`
	JSONValid            bool   `json:"jsonValid"`
	SchemaValid          bool   `json:"schemaValid"`
	MatchesNormalized    bool   `json:"matchesNormalized"`
	LeadingBOM           bool   `json:"leadingBOM"`
	CodeFence            bool   `json:"codeFence"`
}

// A tee observes bytes consumed by the adapter. It neither drains the body nor
// changes an underlying read/close result, and retains at most 256 KiB.
type preflightWireCapture struct {
	mu       sync.Mutex
	data     []byte
	tooLarge bool
	readErr  bool
	eof      bool
}

func (capture *preflightWireCapture) observe(data []byte, err error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	remaining := maxPreflightWireBytes - len(capture.data)
	if len(data) > remaining {
		capture.tooLarge = true
		data = data[:remaining]
	}
	capture.data = append(capture.data, data...)
	if errors.Is(err, io.EOF) {
		capture.eof = true
	} else if err != nil {
		capture.readErr = true
	}
}

func (capture *preflightWireCapture) snapshot() ([]byte, wireCaptureState) {
	data, state := capture.snapshotPrefix()
	if state != wireComplete {
		return nil, state
	}
	return data, state
}

// Partial bodies remain useful to a person inspecting a failed request, but
// must never be mistaken for complete evidence by the wire validator.
func (capture *preflightWireCapture) snapshotPrefix() ([]byte, wireCaptureState) {
	if capture == nil {
		return nil, wireUnavailable
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	state := wireComplete
	switch {
	case capture.tooLarge:
		state = wireTooLarge
	case capture.readErr:
		state = wireReadError
	case !capture.eof:
		state = wireIncomplete
	}
	return append([]byte(nil), capture.data...), state
}

type preflightWireBody struct {
	body    io.ReadCloser
	capture *preflightWireCapture
}

func (body *preflightWireBody) Read(data []byte) (int, error) {
	n, err := body.body.Read(data)
	body.capture.observe(data[:n], err)
	return n, err
}

func (body *preflightWireBody) Close() error { return body.body.Close() }

// GetBody supplies an independent copy; never read or replace the live request
// body. A copy that cannot be captured does not block the actual model request.
func observePreflightWireRequest(request *http.Request) probeWireRequestCheck {
	check := probeWireRequestCheck{Capture: wireUnavailable}
	if request == nil || request.GetBody == nil {
		return check
	}
	body, err := request.GetBody()
	if err != nil {
		check.Capture = wireReadError
		return check
	}
	if body == nil {
		return check
	}
	defer func() { _ = body.Close() }()
	capture := &preflightWireCapture{}
	reader := &preflightWireBody{body: body, capture: capture}
	_, err = io.Copy(io.Discard, io.LimitReader(reader, maxPreflightWireBytes+1))
	if err != nil {
		// The tee records the underlying error. No response error text is kept.
		capture.observe(nil, err)
	}
	data, state := capture.snapshot()
	check.Capture = state
	if state != wireComplete {
		return check
	}
	envelope, state := preflightWireEnvelope(data)
	check.Capture = state
	if state == wireComplete {
		format := observePreflightWireFormat(envelope)
		check.Format = &format
	}
	return check
}

func preflightWireEnvelope(data []byte) (map[string]json.RawMessage, wireCaptureState) {
	if !json.Valid(data) {
		return nil, wireInvalidJSON
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || envelope == nil {
		return nil, wireInvalidEnvelope
	}
	return envelope, wireComplete
}

func observePreflightWireFormat(envelope map[string]json.RawMessage) probeWireFormat {
	result := probeWireFormat{Type: "missing"}
	textRaw, present := envelope["text"]
	if !present || bytes.Equal(bytes.TrimSpace(textRaw), []byte("null")) {
		return result
	}
	var text map[string]json.RawMessage
	if json.Unmarshal(textRaw, &text) != nil || text == nil {
		result.Type = "other"
		return result
	}
	formatRaw, present := text["format"]
	if !present || bytes.Equal(bytes.TrimSpace(formatRaw), []byte("null")) {
		return result
	}
	var format map[string]json.RawMessage
	if json.Unmarshal(formatRaw, &format) != nil || format == nil {
		result.Type = "other"
		return result
	}
	if kindRaw, present := format["type"]; present {
		var kind string
		result.Type = "other"
		if json.Unmarshal(kindRaw, &kind) == nil {
			switch kind {
			case "text", "json_schema", "json_object":
				result.Type = kind
			}
		}
	}
	result.StrictEnabled = bytes.Equal(bytes.TrimSpace(format["strict"]), []byte("true"))
	var actual, expected any
	result.SchemaMatchesProbe = json.Unmarshal(format["schema"], &actual) == nil &&
		json.Unmarshal(probeSchema, &expected) == nil && reflect.DeepEqual(actual, expected)
	return result
}

// This parser independently describes the wire; it never calls the SDK's phase
// projection, selects a substitute answer, or affects the original validator.
func observePreflightWireResponse(capture *preflightWireCapture, normalized string) probeWireResponseCheck {
	data, state := capture.snapshot()
	result := probeWireResponseCheck{Capture: state}
	if state != wireComplete {
		return result
	}
	envelope, state := preflightWireEnvelope(data)
	result.Capture = state
	if state != wireComplete {
		return result
	}
	var outputs []json.RawMessage
	rawOutputs := bytes.TrimSpace(envelope["output"])
	if len(rawOutputs) == 0 || rawOutputs[0] != '[' || json.Unmarshal(rawOutputs, &outputs) != nil {
		result.Capture = wireInvalidEnvelope
		return result
	}
	facts := &probeWireResponseFacts{
		Status: "missing", Format: observePreflightWireFormat(envelope),
		OutputItemCount: len(outputs), Messages: make([]probeWireMessage, 0),
	}
	if rawStatus, present := envelope["status"]; present {
		var status string
		facts.Status = "other"
		if json.Unmarshal(rawStatus, &status) == nil {
			switch status {
			case "completed", "incomplete", "failed":
				facts.Status = status
			}
		}
	}
	validator, compileErr := jsoncontract.Compile(probeSchema)
	validSchema := func(text string) bool {
		return compileErr == nil && validator.Validate(json.RawMessage(text)) == nil
	}
	for index, raw := range outputs {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var kind string
		if json.Unmarshal(item["type"], &kind) != nil || kind != "message" {
			continue
		}
		facts.MessageCount++
		if len(facts.Messages) == maxPreflightWireMessages {
			continue
		}
		message, ok := observePreflightWireMessage(index, item, normalized, validSchema)
		if !ok {
			result.Capture = wireInvalidEnvelope
			return result
		}
		facts.Messages = append(facts.Messages, message)
	}
	facts.MessagesTruncated = len(facts.Messages) < facts.MessageCount
	result.probeWireResponseFacts = facts
	return result
}

func observePreflightWireMessage(index int, item map[string]json.RawMessage, normalized string, validSchema func(string) bool) (probeWireMessage, bool) {
	result := probeWireMessage{Index: index, Phase: "absent"}
	if raw, present := item["phase"]; present {
		result.Phase = "other"
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			result.Phase = "null"
		} else {
			var phase string
			if json.Unmarshal(raw, &phase) == nil && (phase == "commentary" || phase == "final_answer") {
				result.Phase = phase
			}
		}
	}
	var parts []json.RawMessage
	rawParts := bytes.TrimSpace(item["content"])
	if len(rawParts) == 0 || rawParts[0] != '[' || json.Unmarshal(rawParts, &parts) != nil {
		return probeWireMessage{}, false
	}
	var merged strings.Builder
	for _, raw := range parts {
		var part map[string]json.RawMessage
		var kind, text string
		if json.Unmarshal(raw, &part) != nil || json.Unmarshal(part["type"], &kind) != nil {
			result.UnknownPartCount++
			continue
		}
		switch kind {
		case "output_text":
			if json.Unmarshal(part["text"], &text) != nil || bytes.Equal(bytes.TrimSpace(part["text"]), []byte("null")) {
				result.UnknownPartCount++
				continue
			}
			result.TextPartCount++
			merged.WriteString(text)
			if json.Valid([]byte(text)) {
				result.JSONValidPartCount++
			}
			if validSchema(text) {
				result.SchemaValidPartCount++
			}
		case "refusal":
			result.RefusalPartCount++
		default:
			result.UnknownPartCount++
		}
	}
	text := merged.String()
	result.JSONValid = json.Valid([]byte(text))
	result.SchemaValid = validSchema(text)
	result.MatchesNormalized = text == normalized
	leading := strings.TrimLeft(text, " \t\r\n")
	result.LeadingBOM = strings.HasPrefix(leading, "\uFEFF")
	result.CodeFence = strings.HasPrefix(leading, "```") || strings.HasPrefix(leading, "~~~")
	return result, true
}
