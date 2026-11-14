package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type diagnosticTypedCause struct{ detail string }

func (e *diagnosticTypedCause) Error() string { return e.detail }

func TestAttachUpstreamDebugPreservesOriginalCauseAndProjection(t *testing.T) {
	debug := &UpstreamDebugSnapshot{Response: UpstreamDebugResponse{StatusCode: http.StatusServiceUnavailable, Body: "original response"}}
	sentinel := errors.New("original sentinel")
	typed := &diagnosticTypedCause{detail: "private cause"}
	for _, cause := range []error{sentinel, typed, fmt.Errorf("wrapped: %w", typed)} {
		got := attachUpstreamDebug(cause, debug)
		require.ErrorIs(t, got, cause)
		var upstream *UpstreamError
		require.ErrorAs(t, got, &upstream)
		want := &UpstreamError{StatusCode: http.StatusServiceUnavailable, Message: cause.Error(), Body: debug.Response.Body, Debug: debug}
		require.Equal(t, want, upstream)
		require.Equal(t, want.Error(), got.Error())
		expectedJSON, err := json.Marshal(want)
		require.NoError(t, err)
		actualJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.Equal(t, string(expectedJSON), string(actualJSON))
		if errors.Is(cause, typed) {
			var original *diagnosticTypedCause
			require.ErrorAs(t, got, &original)
			require.Same(t, typed, original)
		}
	}
	require.Nil(t, attachUpstreamDebug(nil, debug))
	require.Same(t, typed, attachUpstreamDebug(typed, nil))
	original := &UpstreamError{StatusCode: http.StatusBadRequest, Message: "original", Body: "existing"}
	wrapped := fmt.Errorf("outer: %w", original)
	require.Same(t, wrapped, attachUpstreamDebug(wrapped, debug))
	require.Same(t, debug, original.Debug)
	require.Equal(t, http.StatusBadRequest, original.StatusCode)
	require.Equal(t, "existing", original.Body)
}

func TestBailianQueryCancellationWithDebugPreservesCause(t *testing.T) {
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, expire := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer expire()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"cancelled", cancelled, context.Canceled},
		{"deadline", expired, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			debug := &UpstreamDebugSnapshot{Response: UpstreamDebugResponse{StatusCode: http.StatusServiceUnavailable, Body: "query response"}}
			got := bailianTaskFailure(tc.ctx, bailianFixtureTaskID, errors.New("query failed"), debug)
			require.ErrorIs(t, got, tc.want)
			require.Contains(t, got.Error(), bailianFixtureTaskID)
			require.Contains(t, got.Error(), "task may still exist")
			var upstream *UpstreamError
			require.ErrorAs(t, got, &upstream)
			require.Equal(t, http.StatusServiceUnavailable, upstream.StatusCode)
			require.Equal(t, debug.Response.Body, upstream.Body)
			require.Same(t, debug, upstream.Debug)
			require.ErrorIs(t, bailianTaskFailure(tc.ctx, bailianFixtureTaskID, errors.New("query failed"), nil), tc.want)
		})
	}
}
