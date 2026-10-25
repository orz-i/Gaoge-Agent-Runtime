package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreflightResponseDebugPreservesVisibleBodyAndAnswer(t *testing.T) {
	body := " \n{\"ok\":false,\"n\":9007199254740993}\t"
	answer := " \nThe check did not return JSON.\t "
	capture := &preflightWireCapture{}
	capture.observe([]byte(body), io.EOF)
	got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot(answer, "fake-unused-key")
	if got.Capture != wireComplete || got.Body != body || got.NormalizedAnswer != answer ||
		got.CapturedBytes != len(body) || got.BodyTruncated || got.AnswerTruncated || got.Redacted {
		t.Fatal("failure diagnostic changed non-sensitive body or normalized answer")
	}
	encoded, err := json.Marshal(got)
	var fields map[string]any
	if err != nil || json.Unmarshal(encoded, &fields) != nil || len(fields) != 7 {
		t.Fatal("failure diagnostic changed its seven-field contract")
	}
	original, state := capture.snapshot()
	if state != wireComplete || string(original) != body {
		t.Fatal("debug rendering changed the bytes used by wire validation")
	}
}

func TestPreflightResponseDebugRedactsJSONEscapesNamesAndCredentialFields(t *testing.T) {
	const key = "fake-credential"
	body := " \n{\"message\":\"before \\u0066ake-credential after\",\"\\u0066ake-credential\":\"property\",\"nested\":[{\"Authorization\":\"Bearer external-token\"},{\"api_key\":123},{\"APIKEY\":null},{\"x-api-key\":{\"token\":\"nested\"}},{\"access_token\":\"other-access\"},{\"refresh_token\":\"other-refresh\"}],\"precise\":9007199254740993}\n"
	capture := &preflightWireCapture{}
	capture.observe([]byte(body), io.EOF)
	got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot(" \n"+key+" answer\t", key)
	if !got.Redacted || got.CapturedBytes != len(body) || got.BodyTruncated || got.AnswerTruncated ||
		strings.Contains(got.Body, key) || strings.Contains(got.Body, "external-token") ||
		strings.Contains(got.Body, "other-access") || strings.Contains(got.Body, "other-refresh") ||
		got.NormalizedAnswer != " \n[REDACTED] answer\t" || !json.Valid([]byte(got.Body)) ||
		!strings.Contains(got.Body, "\"precise\":9007199254740993") {
		t.Fatal("JSON escaping, credential fields, answer whitespace or numeric fidelity regressed")
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(got.Body), &decoded) != nil || decoded["message"] != "before [REDACTED] after" ||
		decoded["[REDACTED]"] != "property" {
		t.Fatal("response text or sensitive property-name redaction changed")
	}
	for _, item := range decoded["nested"].([]any) {
		for _, value := range item.(map[string]any) {
			if value != "[REDACTED]" {
				t.Fatal("known credential field retained its value")
			}
		}
	}
	original, _ := capture.snapshot()
	if string(original) != body {
		t.Fatal("redaction modified captured evidence before validation")
	}
}

func TestPreflightResponseDebugKeepsPartialBodyAndWireState(t *testing.T) {
	readErr := errors.New("private transport error must not enter diagnostic")
	for _, tc := range []struct {
		name  string
		state wireCaptureState
		err   error
	}{
		{"incomplete", wireIncomplete, nil},
		{"read_error", wireReadError, readErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "<html>visible error before interruption</html>"
			capture := &preflightWireCapture{}
			capture.observe([]byte(body), tc.err)
			got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot("", "fake-unused-key")
			if got.Capture != tc.state || got.Body != body || got.CapturedBytes != len(body) ||
				got.BodyTruncated || got.Redacted {
				t.Fatal("partial diagnostic hid the observed body or invented truncation")
			}
			wire, state := capture.snapshot()
			if wire != nil || state != tc.state {
				t.Fatal("partial diagnostic promoted incomplete evidence to complete")
			}
			encoded, _ := json.Marshal(got)
			if bytes.Contains(encoded, []byte(readErr.Error())) {
				t.Fatal("raw transport error leaked into the response diagnostic")
			}
		})
	}
	got := (&preflightHTTPObserver{}).responseDebugSnapshot("answer only", "fake-unused-key")
	if got.Capture != wireUnavailable || got.Body != "" || got.CapturedBytes != 0 || got.BodyTruncated ||
		got.NormalizedAnswer != "answer only" {
		t.Fatal("unavailable response invented body facts")
	}
}

func TestPreflightResponseDebugRedactsPartialJSONAndBoundaryKey(t *testing.T) {
	const key = "fake-credential"
	for _, body := range []string{
		"{\"message\":\"visible \\u0066ake-credential after\",\"more\":\"unfinished",
		"{\"message\":\"visible \\u0066ake-creden",
		"{\"message\":\"visible \\u0066ake-creden\\u007",
		"{\"Authorization\":\"Bearer partial-private-token",
		"{\"message\":\"visible \\u0066ake-credential\\q\"}",
		"<html>visible \\u0066ake-credential response</html>",
		"<html>visible \\u0066ake-creden",
	} {
		capture := &preflightWireCapture{}
		capture.observe([]byte(body), nil)
		got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot("", key)
		if !got.Redacted || got.Body == "" || strings.Contains(got.Body, "ake-creden") ||
			strings.Contains(got.Body, "private-token") || got.Capture != wireIncomplete {
			t.Fatal("partial or malformed body hid the prefix or retained an escaped credential")
		}
	}
	prefix := key[:8]
	body := strings.Repeat("z", maxPreflightWireBytes-len(prefix)) + key + " trailing"
	capture := &preflightWireCapture{}
	capture.observe([]byte(body), io.EOF)
	got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot("", key)
	if got.Capture != wireTooLarge || got.CapturedBytes != maxPreflightWireBytes || !got.BodyTruncated ||
		!got.Redacted || strings.Contains(got.Body, prefix) || !strings.HasPrefix(got.Body, strings.Repeat("z", 100)) {
		t.Fatal("capture boundary leaked a partial key or hid other response text")
	}
	wire, state := capture.snapshot()
	if wire != nil || state != wireTooLarge {
		t.Fatal("oversized diagnostic changed the wire-validation state")
	}
	unicodeKey := "fake-界-token"
	unicodePrefix := unicodeKey[:len("fake-")+1]
	capture = &preflightWireCapture{}
	capture.observe([]byte(strings.Repeat("z", maxPreflightWireBytes-len(unicodePrefix))+unicodeKey), io.EOF)
	got = (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot("", unicodeKey)
	if !got.Redacted || strings.Contains(got.Body, "fake-") || !utf8.ValidString(got.Body) {
		t.Fatal("UTF-8 repair hid a truncated credential prefix from redaction")
	}
}

func TestPreflightResponseDebugBoundsUTF8AfterRedaction(t *testing.T) {
	capture := &preflightWireCapture{}
	capture.observe([]byte(strings.Repeat("界", maxPreflightWireBytes/3+1)), io.EOF)
	got := (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot(
		strings.Repeat("界", maxPreflightDebugAnswerBytes/3+1), "fake-unused-key")
	if got.Capture != wireTooLarge || !got.BodyTruncated || !got.AnswerTruncated ||
		len(got.Body) > maxPreflightWireBytes || len(got.NormalizedAnswer) > maxPreflightDebugAnswerBytes ||
		!utf8.ValidString(got.Body) || !utf8.ValidString(got.NormalizedAnswer) {
		t.Fatal("diagnostic limits cut UTF-8 or exceeded byte bounds")
	}
	body := strings.Repeat("x", maxPreflightWireBytes/2)
	capture = &preflightWireCapture{}
	capture.observe([]byte(body), io.EOF)
	got = (&preflightHTTPObserver{responseWire: capture}).responseDebugSnapshot(
		strings.Repeat("x", maxPreflightDebugAnswerBytes), "x")
	if got.Capture != wireComplete || got.CapturedBytes != len(body) || !got.BodyTruncated ||
		!got.AnswerTruncated || !got.Redacted || len(got.Body) > maxPreflightWireBytes ||
		len(got.NormalizedAnswer) > maxPreflightDebugAnswerBytes || strings.Contains(got.Body, "x") {
		t.Fatal("redaction expansion bypassed output byte limits")
	}
}
