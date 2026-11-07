package jsoncontract_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

func TestCanonicalUTF8RejectsEveryInvalidByteInNestedHashInputs(t *testing.T) {
	type candidate struct {
		Config struct {
			Instructions string            `json:"instructions"`
			Labels       map[string]string `json:"labels"`
			Items        []any             `json:"items"`
			Raw          json.RawMessage   `json:"raw"`
		} `json:"config"`
	}
	for raw := 0x80; raw <= 0xff; raw++ {
		bad := string([]byte{byte(raw)})
		for _, location := range []string{"instructions", "map key", "map value", "slice", "raw JSON value", "raw JSON key"} {
			t.Run(fmt.Sprintf("%s/%02x", location, raw), func(t *testing.T) {
				input := candidate{}
				input.Config.Raw = json.RawMessage(`{"ok":true}`)
				switch location {
				case "instructions":
					input.Config.Instructions = "x" + bad
				case "map key":
					input.Config.Labels = map[string]string{"key" + bad: "value"}
				case "map value":
					input.Config.Labels = map[string]string{"key": "value" + bad}
				case "slice":
					input.Config.Items = []any{map[string]any{"nested": []string{"x" + bad}}}
				case "raw JSON value":
					input.Config.Raw = json.RawMessage([]byte(`{"value":"` + bad + `"}`))
				case "raw JSON key":
					input.Config.Raw = json.RawMessage([]byte(`{"key` + bad + `":true}`))
				}
				if err := jsoncontract.ValidateCanonicalUTF8(input); !errors.Is(err, jsoncontract.ErrNonCanonicalUTF8) {
					t.Fatalf("accepted noncanonical %s byte %02x: %v", location, raw, err)
				}
			})
		}
	}
}

func TestCanonicalUTF8ValidUnicodeBinaryAndJSONEscapes(t *testing.T) {
	input := struct {
		Text   string            `json:"text"`
		Items  []string          `json:"items"`
		Labels map[string]string `json:"labels"`
		Raw    json.RawMessage   `json:"raw"`
		Opaque []byte            `json:"opaque"`
	}{
		Text: "世界 🧩", Items: []string{"a", "é", "hello\nworld"},
		Labels: map[string]string{"日本語": "Café"},
		Raw:    json.RawMessage(`{"emoji":"\ud83e\udde9","name":"文字"}`),
		Opaque: []byte{0xff, 0xfe},
	}
	if err := jsoncontract.ValidateCanonicalUTF8(input); err != nil {
		t.Fatalf("valid Unicode or opaque bytes rejected: %v", err)
	}
	if err := jsoncontract.ValidateUniqueJSONObject(input.Raw); err != nil {
		t.Fatalf("valid JSON Unicode pair rejected: %v", err)
	}
}

// Exercise the shared raw JSON parser against random escaping and byte
// sequences; successful inputs must be byte-valid JSON/UTF-8, without a
// lossy Unicode normalization hiding inside encoding/json.
func FuzzCanonicalJSONUnicode(f *testing.F) {
	for _, raw := range [][]byte{
		[]byte("{}"), []byte("null"), []byte("\"café\""),
		[]byte("{\"nested\":[\"日本語\", \"\\ud83e\\udde9\"]}"),
		[]byte("{\"x\":\"\\ud800\"}"), []byte("{\"x\":\"\\udc00\"}"),
		[]byte("{\"x\":\"\\\\ud800\"}"),
		{0xff}, []byte("{\"x\":\"\xff\"}"), []byte(""),
	} {
		f.Add(raw)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			t.Skip()
		}
		err := jsoncontract.ValidateCanonicalJSONUnicode(raw)
		if err == nil {
			if !utf8.Valid(raw) || !json.Valid(raw) {
				t.Fatalf("invalid JSON accepted: %x", raw)
			}
			if err := jsoncontract.ValidateCanonicalUTF8(json.RawMessage(raw)); err != nil {
				t.Fatalf("accepted JSON RawMessage rejected by structural gate: %x err=%v", raw, err)
			}
		}
	})
}

func TestCanonicalUTF8RejectsInvalidEscapedSurrogates(t *testing.T) {
	for _, raw := range []string{
		`{"value":"\ud800"}`,
		`{"value":"\udc00"}`,
		`{"value":"\ud800\ud800"}`,
		`{"value":"\ud800x"}`,
		`{"value":["ok", "\udbff\udfff", "\udfff"]}`,
	} {
		if err := jsoncontract.ValidateUniqueJSONObject([]byte(raw)); !errors.Is(err, jsoncontract.ErrAmbiguousJSON) {
			t.Fatalf("noncanonical JSON surrogate accepted: %s, err=%v", raw, err)
		}
		if err := jsoncontract.ValidateCanonicalUTF8(json.RawMessage(raw)); !errors.Is(err, jsoncontract.ErrNonCanonicalUTF8) {
			t.Fatalf("noncanonical JSON raw accepted: %s, err=%v", raw, err)
		}
	}
}
