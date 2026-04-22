package apxtrace

import (
	"context"
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
