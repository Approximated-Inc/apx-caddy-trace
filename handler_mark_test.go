package apxtrace

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/stretchr/testify/require"
)

func newTraceContextFor(t *testing.T, token string, fr *fakeRedis) *TraceContext {
	t.Helper()
	em := NewEmitter(fr, streamKeyFor(token), 64)
	t.Cleanup(em.Close)
	return &TraceContext{
		Token:        token,
		VhostID:      "vh1",
		RequestID:    "req1",
		StartTime:    time.Now(),
		Emitter:      em,
		LastSnapshot: RequestSnapshot{Method: "GET", URI: "/foo", Headers: http.Header{}},
	}
}

func TestMarkHandler_NoTrace_IsNoop(t *testing.T) {
	fr := newFakeRedis()
	h := &MarkHandler{Phase: "enter", Label: "edge_sequence"}
	r := httptest.NewRequest("GET", "/foo", nil)
	w := httptest.NewRecorder()
	called := false
	next := caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		called = true
		return nil
	})
	require.NoError(t, h.ServeHTTP(w, r, next))
	require.True(t, called)
	require.Equal(t, 0, fr.addCount())
}

func TestMarkHandler_EnterPhase_EmitsEdgeSequenceEntered(t *testing.T) {
	fr := newFakeRedis()
	tc := newTraceContextFor(t, "tok", fr)
	h := &MarkHandler{
		Phase:    "enter",
		Label:    "edge_sequence",
		Metadata: map[string]string{"edge_sequence_id": "42"},
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	r = r.WithContext(withTrace(r.Context(), tc))
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })))

	require.Eventually(t, func() bool { return fr.addCount() == 1 }, time.Second, 10*time.Millisecond)
}

func TestMarkHandler_PostPhase_EmitsMutationWithDiff(t *testing.T) {
	fr := newFakeRedis()
	tc := newTraceContextFor(t, "tok", fr)
	h := &MarkHandler{
		Phase:    "post",
		Label:    "edge_rule",
		Metadata: map[string]string{"edge_rule_id": "117"},
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-Added", "1")
	r = r.WithContext(withTrace(r.Context(), tc))
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(http.ResponseWriter, *http.Request) error { return nil })))

	require.Eventually(t, func() bool { return fr.addCount() == 1 }, time.Second, 10*time.Millisecond)
}
