package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func completionAuthorityFixture() (*modelCompletionAuthority, model.Response) {
	return &modelCompletionAuthority{
		named: true, request: model.Request{RunID: "run", InvocationID: "inv", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("frozen")}}},
		duplicate: func(string) bool { return false },
	}, model.Response{ResponseID: "physical", Usage: &model.Usage{InputTokens: 17, OutputTokens: 5}}
}
func TestModelCompletionAuthoritySourceAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name  string
		run   func(*modelCompletionAuthority, model.Response) error
		valid bool
	}{
		{"validate_cannot_register", func(a *modelCompletionAuthority, r model.Response) error { _, err := a.validate(r, nil); return err }, false},
		{"matching_replay", func(a *modelCompletionAuthority, r model.Response) error {
			_, err := a.replay(a.request, r, nil)
			return err
		}, true},
		{"wrong_invocation_replay", func(a *modelCompletionAuthority, r model.Response) error {
			q := model.CloneRequest(a.request)
			q.InvocationID = "other"
			_, err := a.replay(q, r, nil)
			return err
		}, false},
		{"wrong_run_replay", func(a *modelCompletionAuthority, r model.Response) error {
			q := model.CloneRequest(a.request)
			q.RunID = "other"
			_, err := a.replay(q, r, nil)
			return err
		}, false},
		{"rewritten_request_replay", func(a *modelCompletionAuthority, r model.Response) error {
			q := model.CloneRequest(a.request)
			q.Messages[0].Content = model.TextParts("different")
			_, err := a.replay(q, r, nil)
			return err
		}, false},
		{"physical_then_replay", func(a *modelCompletionAuthority, r model.Response) error {
			_, _ = a.record(r, nil)
			_, err := a.replay(a.request, r, nil)
			return err
		}, false},
		{"replay_then_physical", func(a *modelCompletionAuthority, r model.Response) error {
			_, _ = a.replay(a.request, r, nil)
			_, err := a.record(r, nil)
			return err
		}, false},
		{"duplicate_physical", func(a *modelCompletionAuthority, r model.Response) error {
			_, _ = a.record(r, nil)
			_, err := a.record(r, nil)
			return err
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := completionAuthorityFixture()
			err := tc.run(a, r)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
func TestModelCompletionAuthorityImmutableUsageAndOutcome(t *testing.T) {
	rejected := model.NewResponseRejectedError(errors.New("semantic rejection"))
	for _, tc := range []struct {
		name               string
		sourceError, error error
		mutate             func(*model.Response)
		valid              bool
	}{
		{name: "unchanged_success", valid: true},
		{name: "late_success", error: ErrModelLateResult, valid: true},
		{name: "success_further_rejected", error: rejected, valid: true},
		{name: "joined_rejection", sourceError: rejected, error: errors.Join(rejected, errors.New("accounting")), valid: true},
		{name: "rejection_promoted", sourceError: rejected},
		{name: "unknown_promoted", sourceError: errors.New("transport")},
		{name: "id_rewritten", mutate: func(r *model.Response) { r.ResponseID = "other" }},
		{name: "usage_in_place", mutate: func(r *model.Response) { r.Usage.InputTokens++ }},
		{name: "usage_erased", mutate: func(r *model.Response) { r.Usage = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, r := completionAuthorityFixture()
			_, err := a.record(r, tc.sourceError)
			if tc.sourceError == nil && err != nil {
				t.Fatal(err)
			}
			if tc.mutate != nil {
				tc.mutate(&r)
			}
			_, err = a.validate(r, tc.error)
			if (err == nil || model.IsResponseRejectedError(err) || errors.Is(err, ErrModelLateResult)) != tc.valid {
				t.Fatalf("valid=%t err=%v", tc.valid, err)
			}
		})
	}
}
func TestModelCompletionAuthorityConflictIsStickyAndConcurrent(t *testing.T) {
	a, r := completionAuthorityFixture()
	if _, err := a.record(r, nil); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.validate(r, nil) }()
	}
	bad := model.CloneResponse(r)
	bad.ResponseID = "other"
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = a.validate(bad, nil) }()
	wg.Wait()
	if _, err := a.validate(r, nil); !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("conflict erased: %v", err)
	}
	a, r = completionAuthorityFixture()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.record(r, nil) }()
	}
	wg.Wait()
	if _, err := a.validate(r, nil); !errors.Is(err, ErrInvalidModelResponse) {
		t.Fatalf("concurrent duplicate source accepted: %v", err)
	}
}

type concurrentAuthorityStream struct {
	conflict bool
	late     func(model.StreamEvent) error
}

func (*concurrentAuthorityStream) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}
func (m *concurrentAuthorityStream) GenerateStream(_ context.Context, _ model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	m.late = emit
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "physical"
			if m.conflict && i == 5 {
				id = "other"
			}
			_ = emit(model.StreamEvent{ResponseID: id, Delta: "x"})
		}(i)
	}
	wg.Wait()
	return model.Response{ResponseID: "physical", Content: "done"}, nil
}
func TestConcurrentModelStreamIdentityAndClosedSink(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		client := &concurrentAuthorityStream{conflict: conflict}
		runner := Runner{model: client}
		emitted := 0
		_, err := runner.generateProviderModel(t.Context(), model.Request{}, func(model.StreamEvent) error { emitted++; return nil })
		if (err != nil) != conflict {
			t.Fatalf("conflict=%t err=%v", conflict, err)
		}
		count := emitted
		if err := client.late(model.StreamEvent{Delta: "late"}); !errors.Is(err, ErrInvalidModelResponse) || emitted != count {
			t.Fatalf("closed callback accepted: %v %d/%d", err, emitted, count)
		}
	}
}
