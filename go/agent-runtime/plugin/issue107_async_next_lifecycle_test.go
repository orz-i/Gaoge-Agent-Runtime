package plugin_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type issue107RetainedRunNext struct{ saved plugin.RunNext }

func (m *issue107RetainedRunNext) Name() string { return "issue107.retained_run" }
func (m *issue107RetainedRunNext) Run(_ context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	m.saved = next
	return kernel.Snapshot{}, nil
}

type issue107RetainedModelNext struct{ saved plugin.ModelNext }

func (m *issue107RetainedModelNext) Name() string { return "issue107.retained_model" }
func (m *issue107RetainedModelNext) Model(_ context.Context, _ model.Request, _ model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	m.saved = next
	return model.Response{}, nil
}

type issue107RetainedToolNext struct{ saved plugin.ToolNext }

func (m *issue107RetainedToolNext) Name() string { return "issue107.retained_tool" }
func (m *issue107RetainedToolNext) Tool(_ context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	m.saved = next
	return tools.ExecutionResult{}, nil
}

func TestIssue107MiddlewareCannotInvokeRetainedNextAfterPhysicalCallEnds(t *testing.T) {
	ctx := context.Background()
	t.Run("run", func(t *testing.T) {
		mw := &issue107RetainedRunNext{}
		chain, err := plugin.NewRunChain(mw)
		if err != nil {
			t.Fatal(err)
		}
		called := 0
		_, err = chain.Invoke(ctx, plugin.RunInvocation{}, func(context.Context) (kernel.Snapshot, error) { called++; return kernel.Snapshot{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		_, err = mw.saved(ctx)
		if !errors.Is(err, plugin.ErrNextAlreadyCalled) || called != 0 {
			t.Fatalf("late RunNext dispatched after Grant returned: err=%v called=%d", err, called)
		}
	})
	t.Run("model", func(t *testing.T) {
		mw := &issue107RetainedModelNext{}
		chain, err := plugin.NewModelChain(mw)
		if err != nil {
			t.Fatal(err)
		}
		called := 0
		_, err = chain.Invoke(ctx, model.Request{}, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
			called++
			return model.Response{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		_, err = mw.saved(ctx, model.Request{}, nil)
		if !errors.Is(err, plugin.ErrNextAlreadyCalled) || called != 0 {
			t.Fatalf("late ModelNext escaped parent execution Grant: err=%v called=%d", err, called)
		}
	})
	t.Run("tool", func(t *testing.T) {
		mw := &issue107RetainedToolNext{}
		chain, err := plugin.NewToolChain(mw)
		if err != nil {
			t.Fatal(err)
		}
		called := 0
		_, err = chain.Invoke(ctx, plugin.ToolInvocation{}, func(context.Context) (tools.ExecutionResult, error) { called++; return tools.ExecutionResult{}, nil })
		if err != nil {
			t.Fatal(err)
		}
		_, err = mw.saved(ctx)
		if !errors.Is(err, plugin.ErrNextAlreadyCalled) || called != 0 {
			t.Fatalf("late ToolNext escaped parent execution Grant: err=%v called=%d", err, called)
		}
	})
}

// An asynchronous Next that entered while the middleware was still active
// must complete BEFORE Invoke returns; otherwise its physical callback can
// outlive the enclosing Kernel Child Grant.
type issue107InflightRunNext struct{ started, release chan struct{} }

func (m *issue107InflightRunNext) Name() string { return "issue107.inflight_run" }
func (m *issue107InflightRunNext) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	go func() { _, _ = next(ctx) }()
	<-m.started
	return kernel.Snapshot{}, nil
}
func TestIssue107MiddlewareReturnWaitsForAdmittedInflightNext(t *testing.T) {
	mw := &issue107InflightRunNext{started: make(chan struct{}), release: make(chan struct{})}
	chain, err := plugin.NewRunChain(mw)
	if err != nil {
		t.Fatal(err)
	}
	returned := make(chan struct{})
	go func() {
		_, _ = chain.Invoke(context.Background(), plugin.RunInvocation{}, func(context.Context) (kernel.Snapshot, error) {
			close(mw.started)
			<-mw.release
			return kernel.Snapshot{}, nil
		})
		close(returned)
	}()
	select {
	case <-returned:
		close(mw.release)
		t.Fatal("parent Run Middleware returned before in-flight Next finished: physical Grant could be retired early")
	case <-time.After(20 * time.Millisecond):
	}
	close(mw.release)
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight Next never returned")
	}
}

type issue107InflightModelNext struct{ started, release chan struct{} }

func (m *issue107InflightModelNext) Name() string { return "issue107.inflight_model" }
func (m *issue107InflightModelNext) Model(ctx context.Context, r model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	go func() { _, _ = next(ctx, r, emit) }()
	<-m.started
	return model.Response{}, nil
}

type issue107InflightToolNext struct{ started, release chan struct{} }

func (m *issue107InflightToolNext) Name() string { return "issue107.inflight_tool" }
func (m *issue107InflightToolNext) Tool(ctx context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	go func() { _, _ = next(ctx) }()
	<-m.started
	return tools.ExecutionResult{}, nil
}

func TestIssue107ModelAndToolMiddlewareWaitForInflightPhysicalNext(t *testing.T) {
	t.Run("model", func(t *testing.T) {
		mw := &issue107InflightModelNext{started: make(chan struct{}), release: make(chan struct{})}
		chain, err := plugin.NewModelChain(mw)
		if err != nil {
			t.Fatal(err)
		}
		returned := make(chan struct{})
		go func() {
			_, _ = chain.Invoke(context.Background(), model.Request{}, nil,
				func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
					close(mw.started)
					<-mw.release
					return model.Response{}, nil
				})
			close(returned)
		}()
		select {
		case <-returned:
			close(mw.release)
			t.Fatal("ModelNext escaped enclosing physical Grant")
		case <-time.After(20 * time.Millisecond):
		}
		close(mw.release)
		select {
		case <-returned:
		case <-time.After(3 * time.Second):
			t.Fatal("ModelNext remained blocked")
		}
	})
	t.Run("tool", func(t *testing.T) {
		mw := &issue107InflightToolNext{started: make(chan struct{}), release: make(chan struct{})}
		chain, err := plugin.NewToolChain(mw)
		if err != nil {
			t.Fatal(err)
		}
		returned := make(chan struct{})
		go func() {
			_, _ = chain.Invoke(context.Background(), plugin.ToolInvocation{},
				func(context.Context) (tools.ExecutionResult, error) {
					close(mw.started)
					<-mw.release
					return tools.ExecutionResult{}, nil
				})
			close(returned)
		}()
		select {
		case <-returned:
			close(mw.release)
			t.Fatal("ToolNext escaped enclosing physical Grant")
		case <-time.After(20 * time.Millisecond):
		}
		close(mw.release)
		select {
		case <-returned:
		case <-time.After(3 * time.Second):
			t.Fatal("ToolNext remained blocked")
		}
	})
}
