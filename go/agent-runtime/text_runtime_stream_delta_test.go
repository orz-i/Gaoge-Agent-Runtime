package agentruntime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHasIncompleteProtocolMarkerSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "empty", text: "", want: false},
		{name: "public", text: "Continue the scene carefully.", want: false},
		{name: "html prose", text: "Use the <div> tag in HTML examples.", want: false},
		{name: "lone angle", text: "price is 3<", want: false},
		{name: "dsml open", text: "hello<|", want: true},
		{name: "dsml partial", text: "x<|ds", want: true},
		{name: "tool_call partial", text: "note <tool", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if looksLikeToolProtocolText(tc.text) {
				t.Fatalf("fixture %q is full protocol; incomplete-suffix cases must not include complete markers", tc.text)
			}
			if got := hasIncompleteProtocolMarkerSuffix(tc.text); got != tc.want {
				t.Fatalf("hasIncompleteProtocolMarkerSuffix(%q)=%v want %v", tc.text, got, tc.want)
			}
		})
	}
	// Full markers are classified by looksLikeToolProtocolText; incomplete hold is irrelevant.
	if !looksLikeToolProtocolText(`<|DSML|tool_calls>`) {
		t.Fatal("expected full DSML marker detection")
	}
}

func TestRunDeltaCollectorSuppressesProtocolWithoutPublishing(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		lastFlush: time.Now().Add(-time.Second),
		publishDelta: func(delta string) error {
			published = append(published, delta)
			return nil
		},
	}

	// Cross-chunk DSML: never publish.
	for _, chunk := range []string{"<|", "DSML|", "tool_calls>", "invoke"} {
		if err := collector.accept(GenerateStreamEvent{Delta: chunk}); err != nil {
			t.Fatalf("accept(%q): %v", chunk, err)
		}
	}
	if err := collector.flushFinal(); err != nil {
		t.Fatalf("flushFinal: %v", err)
	}
	if !collector.suppressed {
		t.Fatal("expected suppressed after protocol markup")
	}
	if len(published) != 0 {
		t.Fatalf("protocol deltas leaked: %#v", published)
	}
	if !looksLikeToolProtocolText(collector.content.String()) {
		t.Fatalf("content should retain protocol for classification: %q", collector.content.String())
	}
}

func TestRunDeltaCollectorPublishesPublicThenSuppressesProtocol(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		lastFlush: time.Now().Add(-time.Second),
		publishDelta: func(delta string) error {
			published = append(published, delta)
			return nil
		},
	}

	if err := collector.accept(GenerateStreamEvent{Delta: "Scene continues calmly. "}); err != nil {
		t.Fatal(err)
	}
	// Force mid-stream flush of public text.
	if err := collector.flush(); err != nil {
		t.Fatal(err)
	}
	if err := collector.accept(GenerateStreamEvent{Delta: `<|DSML|tool_calls>invoke`}); err != nil {
		t.Fatal(err)
	}
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}

	if len(published) != 1 || published[0] != "Scene continues calmly. " {
		t.Fatalf("published = %#v, want only public prefix", published)
	}
	joined := strings.Join(published, "")
	if strings.Contains(strings.ToLower(joined), "dsml") || strings.Contains(joined, "tool_calls") {
		t.Fatalf("protocol leaked into deltas: %q", joined)
	}
	if !collector.suppressed {
		t.Fatal("expected suppressed after protocol tail")
	}
}

func TestRunDeltaCollectorStreamsPublicText(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		lastFlush: time.Now().Add(-time.Second),
		publishDelta: func(delta string) error {
			published = append(published, delta)
			return nil
		},
	}
	if err := collector.accept(GenerateStreamEvent{Delta: "Use the <div> tag in HTML examples."}); err != nil {
		t.Fatal(err)
	}
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}
	if collector.suppressed {
		t.Fatal("html prose must not suppress streaming")
	}
	if len(published) != 1 || !strings.Contains(published[0], "<div>") {
		t.Fatalf("published = %#v", published)
	}
}

func TestFinalizeStreamCollectorTextRejectsProtocol(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		lastFlush: time.Now(),
		publishDelta: func(string) error {
			published = append(published, "leaked")
			return nil
		},
	}
	_ = collector.accept(GenerateStreamEvent{Delta: `<|DSML|tool_calls><|DSML|invoke name="story_publish_change_set">`})
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}
	text, err := finalizeStreamCollectorText(collector, &GenerateOutput{Text: collector.content.String()}, "direct")
	if !errors.Is(err, errRequiredToolCallNotProduced) {
		t.Fatalf("err = %v, want errRequiredToolCallNotProduced", err)
	}
	if text != "" {
		t.Fatalf("final text = %q, want empty", text)
	}
	if len(published) != 0 {
		t.Fatalf("protocol published: %#v", published)
	}
}

func TestFinalizeStreamCollectorTextReturnsPublicText(t *testing.T) {
	t.Parallel()
	var published []string
	collector := &runDeltaCollector{
		lastFlush: time.Now().Add(-time.Second),
		publishDelta: func(delta string) error {
			published = append(published, delta)
			return nil
		},
	}
	const answer = "The next beat should raise the stakes."
	_ = collector.accept(GenerateStreamEvent{Delta: answer})
	if err := collector.flushFinal(); err != nil {
		t.Fatal(err)
	}
	text, err := finalizeStreamCollectorText(collector, &GenerateOutput{Text: answer}, "synthesis")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if text != answer {
		t.Fatalf("text = %q", text)
	}
	if len(published) != 1 || published[0] != answer {
		t.Fatalf("published = %#v", published)
	}
}
