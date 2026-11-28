package statehistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestExactHistorySelection(t *testing.T) {
	wire := func(messages, invocations string) json.RawMessage {
		return json.RawMessage(`{"messages":` + messages + `,"modelInvocations":` + invocations + `,"_originalStateEvidence":{"version":"agent-state-opaque-v4","digest":"fixture"},"untouched":{"raw":"\u003c"}}`)
	}
	a := wire(`[{"role":"system","content":"one"},{"role":"user","content":"original"}]`, `[{"status":"consumed","id":"first"},{"status":"completed","id":"active"}]`)
	b := wire(`[{"role":"system","content":"updated"},{"role":"user","content":"original"},{"role":"assistant","content":"answer"}]`, `[{"status":"consumed","id":"first"},{"status":"consumed","id":"active"}]`)
	c := wire(`[{"role":"system","content":"updated"},{"role":"user","content":"compacted"}]`, `[{"status":"consumed","id":"first"},{"status":"consumed","id":"active"}]`)
	type key struct {
		s int
		g uint64
	}
	archive := map[key][]json.RawMessage{}
	read := func(s int, g uint64, n int) ([]json.RawMessage, error) {
		v := archive[key{s, g}]
		if len(v) < n {
			return nil, fmt.Errorf("missing")
		}
		return v[:n], nil
	}
	var previous json.RawMessage
	var m Manifest
	var old Plan
	for i, want := range []json.RawMessage{a, b, c} {
		p := Prepare("agent", previous, want, m, uint64(i+1))
		if i == 1 && (len(p.Appends) != 2 || p.Appends[0].First != 1 || p.Appends[1].First != 1) {
			t.Fatalf("history rewritten: %+v", p.Appends)
		}
		if i == 2 && (len(p.Appends) != 1 || p.Appends[0].Generation != 3 || p.Appends[0].First != 0) {
			t.Fatalf("compaction did not select new history: %+v", p.Appends)
		}
		for _, d := range p.Appends {
			k := key{d.Stream, d.Generation}
			if len(archive[k]) != d.First {
				t.Fatal("duplicate append")
			}
			archive[k] = append(archive[k], d.Entries...)
		}
		hot, parsed, e := OpenPhysical(p.Physical())
		if e != nil {
			t.Fatal(e)
		}
		got, e := Restore(hot, parsed, read)
		if e != nil || !bytes.Equal(got, want) {
			t.Fatalf("original state changed: %s %v", got, e)
		}
		if json.Valid(p.Physical()) {
			t.Fatal("old writer could read partial state")
		}
		if i == 0 {
			old = p
		}
		previous, m = want, p.Manifest
	}
	hot, m, e := OpenPhysical(old.Physical())
	if e != nil {
		t.Fatal(e)
	}
	got, e := Restore(hot, m, read)
	if e != nil || !bytes.Equal(got, a) {
		t.Fatal("historical prefix changed", e)
	}
	archive[key{0, 1}][0] = json.RawMessage(`{"role":"user","content":"foreign"}`)
	if _, e = Restore(hot, m, read); e == nil {
		t.Fatal("foreign original accepted")
	}
	delete(archive, key{0, 1})
	if _, e = Restore(hot, m, read); e == nil {
		t.Fatal("missing original accepted")
	}
	if _, _, e = OpenPhysical([]byte(prefix + "{}\n{}")); e == nil {
		t.Fatal("empty manifest accepted")
	}
	raw := json.RawMessage(`{ "messages": [1, 2] }`)
	if p := Prepare("other", nil, raw, Manifest{}, 1); !bytes.Equal(p.Physical(), raw) || len(p.Appends) != 0 {
		t.Fatal("opaque state changed")
	}
}

func TestFrozenRequestSharesOnlyExactOriginalTranscriptBytes(t *testing.T) {
	item := `{"role":"user","content":"` + strings.Repeat(`original\u003c evidence `, 64) + `"}`
	transcript := `[` + item + `] `
	transcript = strings.TrimSpace(transcript)
	projected := `[{"role":"system","content":"different policy"},` + item + `]`
	other := `[{"role":"user","content":"unrelated projection"}]`
	wire := func(messages, request, business string) json.RawMessage {
		return json.RawMessage(`{"messages":` + messages + `,"modelInvocations":[{"id":"one","status":"pending","request":{"messages":` + request + `},"contextSummary":{"businessRequest":{"messages":` + business + `}}}],"_originalStateEvidence":{"version":"agent-state-opaque-v4","digest":"fixture"}}`)
	}
	original := wire(transcript, projected, transcript)
	p := Prepare("agent", nil, original, Manifest{}, 1)
	if p.Manifest.RequestTails[0] == 0 || p.Manifest.RequestTails[1] == 0 || len(p.Physical()) >= len(original)/2 || len(p.Appends) != 1 || len(p.Appends[0].Entries) != 1 {
		t.Fatalf("frozen original duplicated or not referenced: %+v", p.Manifest)
	}
	hot, m, e := OpenPhysical(p.Physical())
	if e != nil {
		t.Fatal(e)
	}
	read := func(stream int, gen uint64, n int) ([]json.RawMessage, error) {
		d := p.Appends[0]
		if stream != d.Stream || gen != d.Generation || n != len(d.Entries) {
			return nil, fmt.Errorf("wrong original history")
		}
		return d.Entries, nil
	}
	got, e := Restore(hot, m, read)
	if e != nil || !bytes.Equal(got, original) {
		t.Fatal("frozen request changed", e)
	}
	// A projected request whose original messages are no longer in the
	// current transcript must be retained, not rebuilt from the new window.
	changed := wire(other, projected, transcript)
	q := Prepare("agent", original, changed, p.Manifest, 2)
	if q.Manifest.RequestTails != ([2]int{}) {
		t.Fatal("nonmatching original compressed")
	}
	hot, m, e = OpenPhysical(q.Physical())
	if e != nil {
		t.Fatal(e)
	}
	got, e = Restore(hot, m, func(int, uint64, int) ([]json.RawMessage, error) { return q.Appends[0].Entries, nil })
	if e != nil || !bytes.Equal(got, changed) {
		t.Fatal("nonmatching request changed", e)
	}
}
