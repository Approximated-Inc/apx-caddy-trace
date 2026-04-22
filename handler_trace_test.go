package apxtrace

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type fakeRedis struct {
	*fakeStreamer
	arms map[string]string
}

func (f *fakeRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	if v, ok := f.arms[key]; ok {
		cmd.SetVal(v)
	} else {
		cmd.SetErr(redis.Nil)
	}
	return cmd
}

func newFakeRedis() *fakeRedis {
	return &fakeRedis{
		fakeStreamer: &fakeStreamer{},
		arms:         map[string]string{},
	}
}

func nextHandlerOK() caddyhttp.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
		return nil
	}
}

func TestTraceHandler_NoHeader_IsNoop(t *testing.T) {
	fr := newFakeRedis()
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		arm:        fr,
		redactor:   DefaultRedactor(),
		emitterMaker: func(token string) *Emitter {
			return NewEmitter(fr, streamKeyFor(token), 64)
		},
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	w := httptest.NewRecorder()
	err := h.ServeHTTP(w, r, nextHandlerOK())
	require.NoError(t, err)
	require.Equal(t, 0, fr.addCount(), "no events should be written without header")
}

func TestTraceHandler_InvalidToken_IsNoop(t *testing.T) {
	fr := newFakeRedis()
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		arm:        fr,
		redactor:   DefaultRedactor(),
		emitterMaker: func(token string) *Emitter {
			return NewEmitter(fr, streamKeyFor(token), 64)
		},
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", "not-armed-token-0000000000000000")
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))
	require.Equal(t, 0, fr.addCount())
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

func TestTraceHandler_ResolveRedisOpts_InlineConfigWins(t *testing.T) {
	h := &TraceHandler{
		Redis: &RedisConfig{Host: "inline.example", Port: 6390, DB: 2, Password: "secret"},
	}
	opts, err := h.resolveRedisOpts()
	require.NoError(t, err)
	require.Equal(t, "inline.example:6390", opts.Addr)
	require.Equal(t, 2, opts.DB)
	require.Equal(t, "secret", opts.Password)
	require.Nil(t, opts.TLSConfig)
}

func TestTraceHandler_ResolveRedisOpts_TLSEnablesTLSConfig(t *testing.T) {
	h := &TraceHandler{Redis: &RedisConfig{Host: "h", Port: 6379, TLS: true}}
	opts, err := h.resolveRedisOpts()
	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
}

func TestTraceHandler_ResolveRedisOpts_FallsBackToEnv(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "redis://envfallback.example:6380/3")
	h := &TraceHandler{Redis: nil}
	opts, err := h.resolveRedisOpts()
	require.NoError(t, err)
	require.Equal(t, "envfallback.example:6380", opts.Addr)
	require.Equal(t, 3, opts.DB)
}

func TestTraceHandler_ResolveRedisOpts_EmptyHostFallsBackToEnv(t *testing.T) {
	t.Setenv("APX_TRACE_REDIS_URL", "redis://envfallback2.example:6381/0")
	h := &TraceHandler{Redis: &RedisConfig{Port: 1234}} // Host blank
	opts, err := h.resolveRedisOpts()
	require.NoError(t, err)
	require.Equal(t, "envfallback2.example:6381", opts.Addr)
}

func TestTraceHandler_ResolveHeaderName_InlineWins(t *testing.T) {
	h := &TraceHandler{HeaderName: "X-Custom-Trace"}
	require.Equal(t, "X-Custom-Trace", h.resolveHeaderName())
}

func TestTraceHandler_ValidToken_EmitsReceivedAndResponse(t *testing.T) {
	fr := newFakeRedis()
	token := "abcdefabcdefabcdefabcdefabcdefab"
	fr.arms["debug:trace:"+token] = `{"virtual_host_id":"vh1","proxy_server_id":"ps1"}`
	h := &TraceHandler{
		headerName: "X-APX-Debug-Trace",
		arm:        fr,
		redactor:   DefaultRedactor(),
		emitterMaker: func(token string) *Emitter {
			return NewEmitter(fr, streamKeyFor(token), 64)
		},
	}
	r := httptest.NewRequest("GET", "/foo", nil)
	r.Header.Set("X-APX-Debug-Trace", token)
	w := httptest.NewRecorder()
	require.NoError(t, h.ServeHTTP(w, r, nextHandlerOK()))

	// Two events expected: cluster_received + cluster_response.
	require.Eventually(t, func() bool { return fr.addCount() >= 2 }, 2*time.Second, 10*time.Millisecond)
}
