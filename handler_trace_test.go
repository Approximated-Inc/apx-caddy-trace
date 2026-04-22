package apxtrace

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/stretchr/testify/require"
)

// fakeTraceApp is an in-memory AppRef used throughout the test suite.
type fakeTraceApp struct {
	mu     sync.Mutex
	events []Event
	tokens []string
	secret string
}

func newFakeTraceApp(secret string) *fakeTraceApp {
	return &fakeTraceApp{secret: secret}
}

func (f *fakeTraceApp) EmitEvent(evt Event, token string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
	f.tokens = append(f.tokens, token)
}

func (f *fakeTraceApp) Secret() string { return f.secret }

func (f *fakeTraceApp) eventCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakeTraceApp) eventsCopy() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.events))
	copy(out, f.events)
	return out
}

// validTokenFor mints a valid trace token for the given secret + claims.
func validTokenFor(t *testing.T, secret string, payload TokenPayload) string {
	t.Helper()
	if payload.Exp == 0 {
		payload.Exp = time.Now().Add(time.Hour).Unix()
	}
	tok, err := SignToken(payload, secret)
	require.NoError(t, err)
	return tok
}

func nextHandlerOK() caddyhttp.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		return nil
	}
}

func TestTraceHandler_NoHeader_IsNoop(t *testing.T) {
	app := newFakeTraceApp("secret")
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		app:        app,
		redactor:   DefaultRedactor(),
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	w := httptest.NewRecorder()
	err := h.ServeHTTP(w, r, nextHandlerOK())
	require.NoError(t, err)
	require.Equal(t, 0, app.eventCount(), "no events should be emitted without header")
}

func TestTraceHandler_InvalidToken_IsNoop(t *testing.T) {
	app := newFakeTraceApp("secret")
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		app:        app,
		redactor:   DefaultRedactor(),
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", "bogus-not-a-real-token")
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))
	require.Equal(t, 0, app.eventCount())
}

func TestTraceHandler_WrongSecret_IsNoop(t *testing.T) {
	app := newFakeTraceApp("real-secret")
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		app:        app,
		redactor:   DefaultRedactor(),
	}
	// Mint with a different secret — signature mismatch.
	tok := validTokenFor(t, "wrong-secret", TokenPayload{DebugRequestID: "dr-1", VhostID: 1})
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", tok)
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))
	require.Equal(t, 0, app.eventCount())
}

func TestTraceHandler_ExpiredToken_IsNoop(t *testing.T) {
	secret := "s"
	app := newFakeTraceApp(secret)
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		app:        app,
		redactor:   DefaultRedactor(),
	}
	tok := validTokenFor(t, secret, TokenPayload{DebugRequestID: "dr-1", VhostID: 1, Exp: 1})
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", tok)
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))
	require.Equal(t, 0, app.eventCount())
}

type flusherWriter struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flusherWriter) Flush() { f.flushed = true; f.ResponseRecorder.Flush() }

type hijackerWriter struct {
	*httptest.ResponseRecorder
	hijackCalled bool
}

func (h *hijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijackCalled = true
	// Return bogus values; we're only checking the delegation path.
	return nil, nil, nil
}

func TestResponseRecorder_FlushDelegatesToInner(t *testing.T) {
	inner := &flusherWriter{ResponseRecorder: httptest.NewRecorder()}
	rr := &responseRecorder{ResponseWriter: inner, status: 200}
	rr.Flush()
	require.True(t, inner.flushed)
}

func TestResponseRecorder_HijackDelegatesToInner(t *testing.T) {
	inner := &hijackerWriter{ResponseRecorder: httptest.NewRecorder()}
	rr := &responseRecorder{ResponseWriter: inner, status: 200}
	_, _, _ = rr.Hijack()
	require.True(t, inner.hijackCalled)
	require.True(t, rr.hijacked)
}

func TestResponseRecorder_HijackFallsBackIfNotSupported(t *testing.T) {
	rr := &responseRecorder{ResponseWriter: httptest.NewRecorder(), status: 200}
	_, _, err := rr.Hijack()
	require.ErrorIs(t, err, http.ErrNotSupported)
}

func TestResponseRecorder_UnwrapReturnsInner(t *testing.T) {
	inner := httptest.NewRecorder()
	rr := &responseRecorder{ResponseWriter: inner, status: 200}
	require.Equal(t, http.ResponseWriter(inner), rr.Unwrap())
}

func TestTraceHandler_ResolveHeaderName_InlineWins(t *testing.T) {
	h := &TraceHandler{HeaderName: "X-Custom-Trace"}
	require.Equal(t, "X-Custom-Trace", h.resolveHeaderName())
}

func TestTraceHandler_ResolveHeaderName_EnvFallback(t *testing.T) {
	t.Setenv("APX_TRACE_HEADER", "X-From-Env")
	h := &TraceHandler{}
	require.Equal(t, "X-From-Env", h.resolveHeaderName())
}

func TestTraceHandler_ValidToken_EmitsReceivedAndResponse(t *testing.T) {
	secret := "test-secret"
	app := newFakeTraceApp(secret)
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		app:        app,
		redactor:   DefaultRedactor(),
	}
	tok := validTokenFor(t, secret, TokenPayload{DebugRequestID: "dr-xyz", VhostID: 99})

	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", tok)
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))

	require.Equal(t, 2, app.eventCount(), "expected cluster_received + cluster_response")
	events := app.eventsCopy()
	require.Equal(t, EventClusterReceived, events[0].Type)
	require.Equal(t, EventClusterResponse, events[1].Type)
	require.Equal(t, tok, app.tokens[0])
	require.Equal(t, tok, app.tokens[1])
	// request_id should be consistent between received + response.
	require.Equal(t, events[0].Payload["request_id"], events[1].Payload["request_id"])
}
