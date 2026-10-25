package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type wireTestBody struct {
	reader   io.Reader
	readErr  error
	closeErr error
	reads    int
	closes   int
}

func (body *wireTestBody) Read(data []byte) (int, error) {
	body.reads++
	n, err := body.reader.Read(data)
	if body.readErr != nil {
		return n, body.readErr
	}
	return n, err
}

func (body *wireTestBody) Close() error {
	body.closes++
	return body.closeErr
}

func TestPreflightWireTeePreservesReadAndCloseResults(t *testing.T) {
	readErr, closeErr := errors.New("private read error"), errors.New("private close error")
	underlying := &wireTestBody{reader: strings.NewReader("body"), readErr: readErr, closeErr: closeErr}
	capture := &preflightWireCapture{}
	body := &preflightWireBody{body: underlying, capture: capture}
	data := make([]byte, 16)
	n, err := body.Read(data)
	if n != 4 || string(data[:n]) != "body" || !errors.Is(err, readErr) || underlying.reads != 1 || underlying.closes != 0 {
		t.Fatal("tee changed or added a body read")
	}
	if err := body.Close(); !errors.Is(err, closeErr) || underlying.closes != 1 || underlying.reads != 1 {
		t.Fatal("tee changed Close or drained the body")
	}
	assertWireCaptureOnly(t, observePreflightWireResponse(capture, ""), wireReadError)
}

func assertWireCaptureOnly(t *testing.T, check probeWireResponseCheck, want wireCaptureState) {
	t.Helper()
	raw, err := json.Marshal(check)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 1 || fields["capture"] != string(want) {
		t.Fatalf("partial capture invented facts: %s", raw)
	}
}

func TestPreflightWireCaptureBoundsAndPartialStates(t *testing.T) {
	for _, size := range []int{maxPreflightWireBytes, maxPreflightWireBytes + 1, maxPreflightWireBytes + 8192} {
		t.Run(fmt.Sprintf("bytes=%d", size), func(t *testing.T) {
			original := strings.Repeat("x", size)
			underlying := &wireTestBody{reader: strings.NewReader(original)}
			capture := &preflightWireCapture{}
			body := &preflightWireBody{body: underlying, capture: capture}
			got, err := io.ReadAll(body)
			if err != nil || string(got) != original || len(capture.data) > maxPreflightWireBytes {
				t.Fatal("capture truncated the live response or exceeded its memory bound")
			}
			data, state := capture.snapshot()
			if size <= maxPreflightWireBytes {
				if state != wireComplete || len(data) != size {
					t.Fatal("exact-limit capture did not finish")
				}
				data[0] = 'z'
				again, _ := capture.snapshot()
				if again[0] != 'x' {
					t.Fatal("capture snapshot exposed its backing buffer")
				}
			} else {
				assertWireCaptureOnly(t, observePreflightWireResponse(capture, ""), wireTooLarge)
			}
		})
	}
	underlying := &wireTestBody{reader: strings.NewReader("unfinished response")}
	capture := &preflightWireCapture{}
	body := &preflightWireBody{body: underlying, capture: capture}
	if _, err := body.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil || underlying.reads != 1 || underlying.closes != 1 {
		t.Fatal("partial capture consumed extra response data")
	}
	assertWireCaptureOnly(t, observePreflightWireResponse(capture, ""), wireIncomplete)
	assertWireCaptureOnly(t, observePreflightWireResponse(nil, ""), wireUnavailable)
}

func TestPreflightWireRequestReadsOnlyIndependentCopy(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"text": map[string]any{"format": map[string]any{
		"type": "json_schema", "strict": true, "schema": probeSchema,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	live := &wireTestBody{reader: request.Body}
	request.Body = live
	check := observePreflightWireRequest(request)
	if check.Capture != wireComplete || check.Format == nil || check.Format.Type != "json_schema" ||
		!check.Format.StrictEnabled || !check.Format.SchemaMatchesProbe || live.reads != 0 || live.closes != 0 {
		t.Fatal("request copy observation changed live body or lost native format facts")
	}
	got, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatal("actual request body was changed")
	}
}

func TestPreflightWireRequestUnavailableAndInvalidCopies(t *testing.T) {
	for name, request := range map[string]*http.Request{
		"nil":         nil,
		"no_get_body": {},
		"nil_copy":    {GetBody: func() (io.ReadCloser, error) { return nil, nil }},
	} {
		t.Run(name, func(t *testing.T) {
			check := observePreflightWireRequest(request)
			if check.Capture != wireUnavailable || check.Format != nil {
				t.Fatal("unavailable request copy invented format facts")
			}
		})
	}
	check := observePreflightWireRequest(&http.Request{GetBody: func() (io.ReadCloser, error) {
		return nil, errors.New("private request-copy error")
	}})
	if check.Capture != wireReadError || check.Format != nil {
		t.Fatal("copy error invented format facts")
	}
	for _, tc := range []struct {
		name, body string
		state      wireCaptureState
	}{
		{"invalid_json", "{", wireInvalidJSON},
		{"invalid_envelope", "[]", wireInvalidEnvelope},
		{"null_envelope", "null", wireInvalidEnvelope},
		{"oversized", strings.Repeat("private-value", maxPreflightWireBytes/4), wireTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			check := observePreflightWireRequest(request)
			if check.Capture != tc.state || check.Format != nil {
				t.Fatal("invalid request capture invented format facts")
			}
		})
	}
}

func TestPreflightWireResponseRejectsInvalidEnvelopeWithoutPartialFacts(t *testing.T) {
	for _, tc := range []struct {
		body  string
		state wireCaptureState
	}{
		{"{", wireInvalidJSON},
		{"[]", wireInvalidEnvelope},
		{"null", wireInvalidEnvelope},
		{"{}", wireInvalidEnvelope},
		{"{\"output\":null}", wireInvalidEnvelope},
		{"{\"output\":[{\"type\":\"message\",\"content\":{}}]}", wireInvalidEnvelope},
	} {
		capture := &preflightWireCapture{}
		capture.observe([]byte(tc.body), io.EOF)
		assertWireCaptureOnly(t, observePreflightWireResponse(capture, ""), tc.state)
	}
}

func TestPreflightWireResponseCapsMessagesAndKeepsOnlyFixedFacts(t *testing.T) {
	const secret = "private-wire-sentinel"
	output := []any{map[string]any{"type": "reasoning", secret: secret}}
	for i := 0; i < 18; i++ {
		output = append(output, map[string]any{
			"type": "message", "phase": secret, secret: secret,
			"content": []any{
				map[string]any{"type": "output_text", "text": " \n\ufeff" + secret},
				map[string]any{"type": "refusal", "refusal": secret},
				map[string]any{"type": secret, secret: secret},
			},
		})
	}
	raw, err := json.Marshal(map[string]any{
		"status": secret, "text": map[string]any{"format": map[string]any{"type": secret}},
		"output": output, secret: secret,
	})
	if err != nil {
		t.Fatal(err)
	}
	capture := &preflightWireCapture{}
	capture.observe(raw, io.EOF)
	check := observePreflightWireResponse(capture, " \n\ufeff"+secret)
	if check.Capture != wireComplete || check.Status != "other" || check.Format.Type != "other" ||
		check.OutputItemCount != 19 || check.MessageCount != 18 || !check.MessagesTruncated ||
		len(check.Messages) != maxPreflightWireMessages {
		t.Fatal("wire summary lost bounded envelope counts")
	}
	for i, message := range check.Messages {
		if message.Index != i+1 || message.Phase != "other" || message.TextPartCount != 1 ||
			message.RefusalPartCount != 1 || message.UnknownPartCount != 1 || !message.LeadingBOM ||
			message.CodeFence || !message.MatchesNormalized || message.JSONValid || message.SchemaValid {
			t.Fatal("wire message facts changed shape or interpretation")
		}
	}
	encoded, err := json.Marshal(check)
	if err != nil || bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("wire summary retained untrusted property names, values or text")
	}
}
