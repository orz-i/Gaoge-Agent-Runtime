package harness

import "testing"

func TestSkillReadArgumentsAreUnambiguousButAllowExplicitZeroOffset(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		ok            bool
		offset, limit int
	}{
		{"omitted optional fields", `{"id":"doc"}`, true, 0, 0},
		{"explicit zero offset", `{"id":"doc","offset":0,"limit":8000}`, true, 0, 8000},
		{"unicode", `{"id":"文档","offset":2,"limit":17}`, true, 2, 17},
		{"duplicate ID", `{"id":"a","id":"b"}`, false, 0, 0},
		{"duplicate offset", `{"id":"doc","offset":0,"offset":1}`, false, 0, 0},
		{"alias capitalization", `{"ID":"doc"}`, false, 0, 0},
		{"unknown field", `{"id":"doc","foreign":true}`, false, 0, 0},
		{"decimal offset", `{"id":"doc","offset":1.5}`, false, 0, 0},
		{"overflow offset", `{"id":"doc","offset":9223372036854775808}`, false, 0, 0},
		{"negative offset", `{"id":"doc","offset":-1}`, false, 0, 0},
		{"string offset", `{"id":"doc","offset":"0"}`, false, 0, 0},
		{"null ID", `{"id":null}`, false, 0, 0},
		{"null offset", `{"id":"doc","offset":null}`, false, 0, 0},
		{"null limit", `{"id":"doc","limit":null}`, false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, valid := decodeSkillReadInput([]byte(tc.raw))
			if valid != tc.ok || tc.ok && (got.Offset != tc.offset || got.Limit != tc.limit) {
				t.Fatalf("parsed ambiguous input: %+v valid=%t", got, valid)
			}
		})
	}
}
