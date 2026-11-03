package openai

import "testing"

func TestEqualJSONNestedObjectOrder(t *testing.T) {
	a := []byte(`{"annotations":[{"type":"url_citation","url":"https://example.com","title":"x"}]}`)
	b := []byte(`{"annotations":[{"title":"x","url":"https://example.com","type":"url_citation"}]}`)
	if !equalJSON(a, b) {
		t.Fatal("equivalent nested objects differ")
	}
	if equalJSON(a, []byte(`{"annotations":[{"type":"other"}]}`)) {
		t.Fatal("different evidence matched")
	}
}
