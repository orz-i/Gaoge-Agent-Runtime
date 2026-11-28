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
	Spans        [2]Span
	RequestTails [2]int `json:"RequestTails,omitzero"`
	Digest       [32]byte
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
	hot          json.RawMessage
	spans        [2]Span
	entries      [2][]json.RawMessage
	requestTails [2]int
}

// field preserves byte offsets inside the original JSON object. Never
// marshal an object again: raw provider evidence and ordering are original.
type field struct {
	start, end int
	value      json.RawMessage
}

func fields(raw []byte) map[string]field {
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, e := dec.Token()
	if e != nil || t != json.Delim('{') {
		return nil
	}
	out := make(map[string]field)
	for dec.More() {
		key, e := dec.Token()
		if e != nil {
			return nil
		}
		var v json.RawMessage
		if e = dec.Decode(&v); e != nil {
			return nil
		}
		end := int(dec.InputOffset())
		name := key.(string)
		if _, exists := out[name]; exists {
			return nil
		}
		out[name] = field{end - len(v), end, v}
	}
	return out
}
func exactArray(f field) []json.RawMessage {
	var all []json.RawMessage
	if json.Unmarshal(f.value, &all) != nil || !bytes.Equal(array(all), f.value) {
		return nil
	}
	return all
}
func replace(raw []byte, f field, v []byte) []byte {
	out := make([]byte, 0, len(raw)+len(v)-(f.end-f.start))
	out = append(out, raw[:f.start]...)
	out = append(out, v...)
	return append(out, raw[f.end:]...)
}

// Both the physical request and the summary's frozen business request may
// share the exact transcript suffix of this same logical Run revision.
// Never ask a model, Host, or a later State to reconstruct an original.
// Retain all differing messages and unrecognized projections verbatim.
var requestMessagePaths = [2][]string{{"request", "messages"}, {"contextSummary", "businessRequest", "messages"}}

func requestMessages(inv []byte, path []string) field {
	var offset int
	for _, name := range path {
		f := fields(inv)[name]
		if len(f.value) == 0 {
			return field{}
		}
		offset += f.start
		inv = f.value
	}
	return field{offset, offset + len(inv), inv}
}
func activeInvocation(inv []byte) bool {
	switch string(fields(inv)["status"].value) {
	case `"pending"`, `"completed"`:
		return true
	}
	return false
}

// Replace the shared raw suffix by a byte count; the nonmatching original
// prefix remains encoded in the hot field. This is an exact byte reference,
// not an inference about the message's meaning or a new history stream.
func compactRequests(inv, transcript json.RawMessage) (json.RawMessage, [2]int) {
	var tails [2]int
	if !activeInvocation(inv) {
		return inv, tails
	}
	for i, path := range requestMessagePaths {
		f := requestMessages(inv, path)
		original := f.value
		n := 0
		for n < len(original) && n < len(transcript) && original[len(original)-1-n] == transcript[len(transcript)-1-n] {
			n++
		}
		if n <= 64 {
			continue
		}
		head, _ := json.Marshal([]byte(original[:len(original)-n]))
		if len(head)+16 < len(original) {
			tails[i] = n
			inv = replace(inv, f, head)
		}
	}
	return inv, tails
}
func restoreRequests(state json.RawMessage, tails [2]int) (json.RawMessage, error) {
	if tails == ([2]int{}) {
		return state, nil
	}
	root := fields(state)
	transcript := root["messages"].value
	list := exactArray(root["modelInvocations"])
	if len(list) == 0 || !activeInvocation(list[len(list)-1]) {
		return nil, fmt.Errorf("missing original active request")
	}
	inv := list[len(list)-1]
	for i, n := range tails {
		if n == 0 {
			continue
		}
		f := requestMessages(inv, requestMessagePaths[i])
		var head []byte
		if n < 0 || n > len(transcript) || json.Unmarshal(f.value, &head) != nil {
			return nil, fmt.Errorf("invalid original request reference")
		}
		inv = replace(inv, f, append(head, transcript[len(transcript)-n:]...))
	}
	list[len(list)-1] = inv
	return replace(state, root["modelInvocations"], array(list)), nil
}

// Only the first-party sealed Agent wire is partitioned. Other feature
// states remain opaque, including unsupported/noncanonical arrays.
func decompose(kind string, state json.RawMessage) split {
	raw := split{hot: state}
	if kind != "agent" {
		return raw
	}
	root := fields(state)
	var evidence struct{ Version string }
	if json.Unmarshal(root["_originalStateEvidence"].value, &evidence) != nil || evidence.Version != "agent-state-opaque-v3" {
		return raw
	}
	transcript := root["messages"].value
	pos := 0
	var hot []byte
	for i, name := range []string{"messages", "modelInvocations"} {
		f := root[name]
		if len(f.value) == 0 || f.start < pos {
			continue
		}
		all := exactArray(f)
		if len(all) == 0 {
			continue
		}
		n := 0
		for j, item := range all {
			var tag struct{ Role, Status string }
			if json.Unmarshal(item, &tag) != nil {
				return split{hot: state}
			}
			if i == 0 {
				if tag.Role == "system" {
					n = j + 1
				}
			} else if tag.Status == "consumed" {
				n = j + 1
			} else {
				break
			}
		}
		var retained, archived []json.RawMessage
		if i == 0 {
			retained, archived = all[:n], all[n:]
		} else {
			archived, retained = all[:n], all[n:]
			if len(retained) > 0 {
				last := len(retained) - 1
				retained[last], raw.requestTails = compactRequests(retained[last], transcript)
			}
		}
		if len(archived) == 0 && raw.requestTails == ([2]int{}) {
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
	p := Plan{Hot: bytes.Clone(current.hot), Manifest: Manifest{Spans: current.spans, RequestTails: current.requestTails, Digest: sha256.Sum256(next)}}
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

func (m Manifest) Empty() bool {
	return m.Spans[0].Count == 0 && m.Spans[1].Count == 0 && m.RequestTails == ([2]int{})
}

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
	result, e := restoreRequests(result, m.RequestTails)
	if e != nil {
		return nil, e
	}
	if !json.Valid(result) || sha256.Sum256(result) != m.Digest {
		return nil, fmt.Errorf("invalid original state history digest")
	}
	return result, nil
}
