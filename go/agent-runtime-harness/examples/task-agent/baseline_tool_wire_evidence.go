package main

import (
	"bytes"
	"encoding/json"
	"strings"
)

// toolProbeWireEvidence is an informational, content-free observation of SSE
// bytes the Provider adapter already consumed. It does NOT authorize a Tool,
// settle usage, or weaken the original Provider parser. Ciphertext, IDs and
// event bodies never enter reports.
type toolProbeWireEvidence struct {
	ReasoningDone  bool  `json:"reasoningDone"`
	EncryptedDone  bool  `json:"encryptedDone"`
	TerminalSeen   bool  `json:"terminalSeen"`
	DoneFinalMatch *bool `json:"doneFinalMatch,omitempty"`
}

type encryptedItemEvidence struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	EncryptedContent string `json:"encrypted_content"`
}

// observeToolProbeWireEvidence runs only for a failed probe, using a bounded
// already-observed prefix. Missing events are unknown, not negative protocol
// proof. Only an actual done/final pair for the same item ID is compared.
func observeToolProbeWireEvidence(observer *preflightHTTPObserver) *toolProbeWireEvidence {
	if observer == nil {
		return nil
	}
	observer.mu.Lock()
	capture := observer.responseWire
	observer.mu.Unlock()
	if capture == nil {
		return nil
	}
	raw, _ := capture.snapshotPrefix()
	// The observer's own cap is 256 KiB. Never read more from the live response.
	if len(raw) == 0 {
		return nil
	}
	type doneEvidence struct {
		index int
		encryptedItemEvidence
	}
	var done []doneEvidence
	var finalItems []json.RawMessage
	evidence := &toolProbeWireEvidence{}
	observed := false
	for _, frame := range bytes.Split(raw, []byte("\n\n")) {
		var kind string
		var body []byte
		for _, line := range bytes.Split(frame, []byte("\n")) {
			if bytes.HasPrefix(line, []byte("event: ")) {
				kind = string(bytes.TrimSpace(line[7:]))
			}
			if bytes.HasPrefix(line, []byte("data: ")) {
				body = line[6:]
			}
		}
		if len(body) == 0 {
			continue
		}
		switch kind {
		case "response.output_item.done":
			var wrapper struct {
				Type        string                `json:"type"`
				OutputIndex *int                  `json:"output_index"`
				Item        encryptedItemEvidence `json:"item"`
			}
			if json.Unmarshal(body, &wrapper) != nil || wrapper.Type != kind || wrapper.OutputIndex == nil ||
				*wrapper.OutputIndex < 0 || *wrapper.OutputIndex >= 16 || wrapper.Item.Type != "reasoning" ||
				strings.TrimSpace(wrapper.Item.ID) == "" {
				continue
			}
			observed = true
			evidence.ReasoningDone = true
			evidence.EncryptedDone = evidence.EncryptedDone || wrapper.Item.EncryptedContent != ""
			done = append(done, doneEvidence{index: *wrapper.OutputIndex, encryptedItemEvidence: wrapper.Item})
		case "response.completed":
			var wrapper struct {
				Type     string `json:"type"`
				Response struct {
					Output []json.RawMessage `json:"output"`
				} `json:"response"`
			}
			if json.Unmarshal(body, &wrapper) != nil || wrapper.Type != kind {
				continue
			}
			observed = true
			evidence.TerminalSeen = true
			finalItems = wrapper.Response.Output
		}
	}
	if !observed {
		return nil
	}
	for _, d := range done {
		if d.EncryptedContent == "" || d.index >= len(finalItems) {
			continue
		}
		var final encryptedItemEvidence
		if json.Unmarshal(finalItems[d.index], &final) != nil ||
			final.Type != "reasoning" || final.ID != d.ID || final.EncryptedContent == "" {
			continue
		}
		match := final.EncryptedContent == d.EncryptedContent
		if evidence.DoneFinalMatch == nil {
			evidence.DoneFinalMatch = &match
		} else if !match {
			*evidence.DoneFinalMatch = false
		}
	}
	return evidence
}
