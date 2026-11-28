// Package statehistory implements the physical representation of sealed
// Agent state. It never changes the logical State, execution proofs, or
// Store.Commit contract. Storage adapters retain immutable array entries
// and replace only the hot state and its ordered history selection.
package statehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// A span selects one exact ordered history prefix or suffix. Offsets refer
// to the hot JSON array. Generation is the revision at which that selected
// stream was first written; entries before Count are immutable.
type Span struct {
	Start      int
	End        int
	Generation uint64
	Count      int
	Prefix     bool
}
type Manifest struct {
	Spans  [2]Span
	Digest [32]byte
}
type Append struct {
	Stream     int
	Generation uint64
	First      int
	Entries    []json.RawMessage
}
type Plan struct {
	Hot      json.RawMessage
	Manifest Manifest
	Appends  []Append
}

type split struct {
	hot     json.RawMessage
	spans   [2]Span
	entries [2][]json.RawMessage
}

// decompose only recognizes the first-party sealed Agent wire. Other
// feature state remains opaque. Keep the actual encoded bytes, including
// key order and raw JSON spelling; never marshal a typed state again.
func decompose(kind string, state json.RawMessage) split {
	raw := split{hot: state}
	if kind != "agent" {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(state))
	t, e := dec.Token()
	if e != nil || t != json.Delim('{') {
		return raw
	}
	type field struct {
		start, end int
		value      json.RawMessage
	}
	var fields [2]field
	sealed := false
	for dec.More() {
		key, e := dec.Token()
		if e != nil {
			return raw
		}
		var v json.RawMessage
		if e = dec.Decode(&v); e != nil {
			return raw
		}
		end := int(dec.InputOffset())
		switch key {
		case "_originalStateEvidence":
			var evidence struct{ Version string }
			if json.Unmarshal(v, &evidence) == nil && evidence.Version == "agent-state-opaque-v3" {
				sealed = true
			}
		case "messages":
			fields[0] = field{end - len(v), end, v}
		case "modelInvocations":
			fields[1] = field{end - len(v), end, v}
		}
	}
	if !sealed {
		return raw
	}
	// In the canonical Agent encoding messages precede model invocations.
	// Unrecognized/noncanonical layout is retained exactly as opaque bytes.
	pos := 0
	var hot []byte
	for i, f := range fields {
		if len(f.value) == 0 || f.start < pos {
			continue
		}
		var all []json.RawMessage
		if json.Unmarshal(f.value, &all) != nil || len(all) == 0 || !bytes.Equal(array(all), f.value) {
			continue
		}
		n := 0
		if i == 0 {
			// The system guidance is mutable. Archive only the suffix after the
			// last system entry; compaction may select a different generation.
			for j, item := range all {
				var x struct{ Role string }
				if json.Unmarshal(item, &x) != nil {
					return split{hot: state}
				}
				if x.Role == "system" {
					n = j + 1
				}
			}
		} else {
			for _, item := range all {
				var x struct{ Status string }
				if json.Unmarshal(item, &x) != nil {
					return split{hot: state}
				}
				if x.Status != "consumed" {
					break
				}
				n++
			}
		}
		var retained, archived []json.RawMessage
		if i == 0 {
			retained, archived = all[:n], all[n:]
		} else {
			archived, retained = all[:n], all[n:]
		}
		if len(archived) == 0 {
			continue
		}
		hot = append(hot, state[pos:f.start]...)
		start := len(hot)
		hot = append(hot, array(retained)...)
		raw.spans[i] = Span{Start: start, End: len(hot), Count: len(archived), Prefix: i == 1}
		raw.entries[i] = archived
		pos = f.end
	}
	if pos != 0 {
		raw.hot = append(hot, state[pos:]...)
	}
	return raw
}

func array(items []json.RawMessage) []byte {
	n := 2
	for _, item := range items {
		n += len(item) + 1
	}
	result := make([]byte, 0, n)
	result = append(result, '[')
	for i, item := range items {
		if i != 0 {
			result = append(result, ',')
		}
		result = append(result, item...)
	}
	return append(result, ']')
}

// Prepare compares the exact archived entries against the previous logical
// state. A changed prefix (e.g. context compaction) selects a new generation;
// only identical immutable entries may be reused. A nil previous manifest
// means a new Run or a prior revision with no archived entries.
func Prepare(kind string, previous, next json.RawMessage, old Manifest, revision uint64) Plan {
	current := decompose(kind, next)
	var before split
	if !old.Empty() {
		before = decompose(kind, previous)
	}
	p := Plan{Hot: bytes.Clone(current.hot), Manifest: Manifest{Spans: current.spans, Digest: sha256.Sum256(next)}}
	for i, items := range current.entries {
		if len(items) == 0 {
			continue
		}
		prior := old.Spans[i]
		first := 0
		generation := revision
		if prior.Count > 0 && prior.Count == len(before.entries[i]) && prior.Count <= len(items) {
			equal := true
			for j, v := range before.entries[i] {
				if !bytes.Equal(v, items[j]) {
					equal = false
					break
				}
			}
			if equal {
				generation, first = prior.Generation, prior.Count
			}
		}
		p.Manifest.Spans[i].Generation = generation
		if first < len(items) {
			p.Appends = append(p.Appends, Append{Stream: i, Generation: generation, First: first, Entries: items[first:]})
		}
	}
	return p
}

func (m Manifest) Empty() bool { return m.Spans[0].Count == 0 && m.Spans[1].Count == 0 }

// A partitioned State has a non-JSON version marker, a compact manifest,
// and the exact hot JSON bytes. A missing manifest or an old writer cannot
// reinterpret the partial JSON as the original logical State.
const prefix = "#agent-state-history-v1\n"

func (p Plan) Physical() json.RawMessage {
	if p.Manifest.Empty() {
		return p.Hot
	}
	metadata, _ := json.Marshal(p.Manifest)
	out := append([]byte(prefix), metadata...)
	out = append(out, '\n')
	return append(out, p.Hot...)
}

// OpenPhysical returns the selected immutable manifest and hot bytes.
// Other feature State remains untouched and must itself be valid JSON.
func OpenPhysical(raw json.RawMessage) (json.RawMessage, Manifest, error) {
	var m Manifest
	if !bytes.HasPrefix(raw, []byte(prefix)) {
		if !json.Valid(raw) {
			return nil, m, fmt.Errorf("invalid persisted state")
		}
		return raw, m, nil
	}
	metadata, hot, ok := bytes.Cut(raw[len(prefix):], []byte{'\n'})
	if !ok || json.Unmarshal(metadata, &m) != nil || m.Empty() || !json.Valid(hot) {
		return nil, m, fmt.Errorf("invalid state history envelope")
	}
	return hot, m, nil
}

// Restore reconstructs the exact original State, verifies its digest, and
// never substitutes missing history. Reads select exactly the count bound
// to the observed Run revision, even if a newer writer appended more.
func Restore(hot json.RawMessage, m Manifest, read func(int, uint64, int) ([]json.RawMessage, error)) (json.RawMessage, error) {
	if m.Empty() {
		return bytes.Clone(hot), nil
	}
	pos := 0
	result := make([]byte, 0, len(hot))
	for i, s := range m.Spans {
		if s.Count == 0 {
			continue
		}
		if s.Count < 0 || s.Generation == 0 || s.Start < pos || s.End <= s.Start || s.End > len(hot) {
			return nil, fmt.Errorf("invalid state history span")
		}
		entries, e := read(i, s.Generation, s.Count)
		if e != nil {
			return nil, e
		}
		if len(entries) != s.Count {
			return nil, fmt.Errorf("incomplete state history")
		}
		var retained []json.RawMessage
		if e = json.Unmarshal(hot[s.Start:s.End], &retained); e != nil {
			return nil, e
		}
		result = append(result, hot[pos:s.Start]...)
		var all []json.RawMessage
		if s.Prefix {
			all = append(append(all, entries...), retained...)
		} else {
			all = append(append(all, retained...), entries...)
		}
		result = append(result, array(all)...)
		pos = s.End
	}
	result = append(result, hot[pos:]...)
	if !json.Valid(result) || sha256.Sum256(result) != m.Digest {
		return nil, fmt.Errorf("invalid original state history digest")
	}
	return result, nil
}
