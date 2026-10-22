package apxtrace

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp/reverseproxy"
	_ "github.com/caddyserver/caddy/v2/modules/filestorage"
	"github.com/stretchr/testify/require"
)

// Caddy 2.11.6+ hands every request to the route under an IdleTimeoutWriter
// (write_idle_timeout defaults to 1m). It offers FlushError and Unwrap but
// no Flush or Hijack, so only a recorder that walks Unwrap reaches the
// net/http writer that can flush, hijack or push.

// unwrapOnlyWriter is that shape, minus even the Push the real one promotes
// from caddyhttp.ResponseWriterWrapper.
type unwrapOnlyWriter struct{ w http.ResponseWriter }

func (u *unwrapOnlyWriter) Header() http.Header         { return u.w.Header() }
func (u *unwrapOnlyWriter) Write(b []byte) (int, error) { return u.w.Write(b) }
func (u *unwrapOnlyWriter) WriteHeader(code int)        { u.w.WriteHeader(code) }
func (u *unwrapOnlyWriter) Unwrap() http.ResponseWriter { return u.w }
func (u *unwrapOnlyWriter) FlushError() error           { return http.NewResponseController(u.w).Flush() }

// bareWriter can do nothing beyond http.ResponseWriter.
type bareWriter struct{ http.ResponseWriter }

// pushWriter is an HTTP/2-style writer that records server pushes.
type pushWriter struct {
	http.ResponseWriter
	pushed []string
}

func (p *pushWriter) Push(target string, _ *http.PushOptions) error {
	p.pushed = append(p.pushed, target)
	return nil
}

// pipeHijackWriter is an HTTP/1-style writer that hands over one end of a pipe.
type pipeHijackWriter struct {
	http.ResponseWriter
	conn net.Conn
}

func (h *pipeHijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return h.conn, bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn)), nil
}

const testTraceSecret = "test-secret"

func TestRecorderFlushReachesWriterBehindUnwrapOnlyWrapper(t *testing.T) {
	// Direct Flush serves callers that type-assert http.Flusher; Caddy's
	// reverse_proxy flushes through http.ResponseController.
	for name, flush := range map[string]func(*responseRecorder) error{
		"Flush":              func(rr *responseRecorder) error { rr.Flush(); return nil },
		"ResponseController": func(rr *responseRecorder) error { return http.NewResponseController(rr).Flush() },
	} {
		t.Run(name, func(t *testing.T) {
			inner := httptest.NewRecorder()
			started := 0
			rr := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{inner}, status: 200, onFirstWrite: func() { started++ }}
			require.NoError(t, flush(rr))
			require.NoError(t, flush(rr))
			require.True(t, inner.Flushed, "the flush must reach the writer under the wrapper")
			require.Equal(t, 1, started, "the first flush starts the response, once")
		})
	}
}

func TestRecorderFlushOnUnflushableChainDoesNotStartResponse(t *testing.T) {
	started := 0
	rr := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{bareWriter{httptest.NewRecorder()}}, status: 200, onFirstWrite: func() { started++ }}
	require.ErrorIs(t, http.NewResponseController(rr).Flush(), http.ErrNotSupported)
	rr.Flush()
	require.False(t, rr.wrote, "a flush that cannot happen must not swallow a later WriteHeader")
	require.Zero(t, started)
}

func TestRecorderPushReachesPusherBehindUnwrapOnlyWrapper(t *testing.T) {
	inner := &pushWriter{ResponseWriter: httptest.NewRecorder()}
	rr := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{inner}, status: 200}
	require.NoError(t, rr.Push("/app.css", nil))
	require.Equal(t, []string{"/app.css"}, inner.pushed)

	noPusher := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{httptest.NewRecorder()}, status: 200}
	require.ErrorIs(t, noPusher.Push("/app.css", nil), http.ErrNotSupported)
}

func TestRecorderHijackReachesHijackerBehindUnwrapOnlyWrapper(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	inner := &pipeHijackWriter{ResponseWriter: httptest.NewRecorder(), conn: server}
	rr := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{inner}, status: 200}
	rr.WriteHeader(http.StatusSwitchingProtocols)

	conn, brw, err := http.NewResponseController(rr).Hijack()
	require.NoError(t, err)
	require.Equal(t, server, conn)
	require.NotNil(t, brw)
	require.True(t, rr.hijacked)
}

func TestRecorderHijackUnsupportedLeavesResponseUnhijacked(t *testing.T) {
	rr := &responseRecorder{ResponseWriter: &unwrapOnlyWriter{httptest.NewRecorder()}, status: 200}
	_, _, err := rr.Hijack()
	require.ErrorIs(t, err, http.ErrNotSupported)
	require.False(t, rr.hijacked)
	require.False(t, rr.wrote)
}

// The tests below run a traced request through the trace handler in front
// of Caddy's own reverse_proxy, under the unwrapOnlyWriter.

func TestTraceHandlerWebSocketUpgradeThroughUnwrapOnlyWriter(t *testing.T) {
	app := newFakeTraceApp(testTraceSecret)
	tok := validTokenFor(t, testTraceSecret, TokenPayload{DebugRequestID: "dr-ws", VhostID: 7})
	edge, done := traceEdge(t, app, caddyProxy(t, upgradeEchoUpstream(t)), false)

	exchangeWebSocketFrames(t, edge.Listener.Addr().String(), "/socket", tok)
	waitHandled(t, done)

	resp := findEvent(t, app.eventsCopy(), EventClusterResponse)
	require.Equal(t, true, resp.Payload["hijacked"])
	require.NotContains(t, resp.Payload, "status", "a hijacked response has no status or headers to report")
}

func TestTraceHandlerSSEFlushesThroughUnwrapOnlyWriter(t *testing.T) {
	for _, proto := range []string{"HTTP/1.1", "HTTP/2.0"} {
		t.Run(proto, func(t *testing.T) {
			upstream, release, stalled := sseUpstream(t)
			app := newFakeTraceApp(testTraceSecret)
			tok := validTokenFor(t, testTraceSecret, TokenPayload{DebugRequestID: "dr-sse", VhostID: 7})
			edge, done := traceEdge(t, app, caddyProxy(t, upstream), proto == "HTTP/2.0")

			readSSE(t, edge.Client(), edge.URL+"/events", proto, tok, release, stalled)
			waitHandled(t, done)

			events := app.eventsCopy()
			started := findEvent(t, events, EventClusterResponseStarted)
			resp := findEvent(t, events, EventClusterResponse)
			require.Equal(t, http.StatusOK, resp.Payload["status"])
			require.GreaterOrEqual(t, resp.Payload["bytes_written"], len(sseBody))
			require.LessOrEqual(t, started.TsNs, resp.TsNs)
		})
	}
}

// TestCaddyServerTracesStreamsWithoutStats loads the trace handler into a
// real Caddy HTTP server with no apx_stats handler around it, so it sits
// directly on whatever writer that Caddy version installs: nothing on
// 2.11.4, IdleTimeoutWriter on 2.11.6+. In the fleet, stats wraps trace and
// hides the trace recorder's own type assertions; this is the case where
// tracing is on and stats is off. Events go out through the real sink path.
func TestCaddyServerTracesStreamsWithoutStats(t *testing.T) {
	const secret = "e2e-trace-secret"
	t.Setenv("APX_TRACE_E2E_KEY", secret)
	sink, sunk := traceEventSink(t)
	sse, release, stalled := sseUpstream(t)
	route := func(path string, upstream *httptest.Server) map[string]any {
		return map[string]any{
			"match":  []any{map[string]any{"path": []string{path}}},
			"handle": []any{map[string]any{"handler": "reverse_proxy", "upstreams": []any{map[string]any{"dial": upstream.Listener.Addr().String()}}}},
		}
	}
	addr := freeLoopbackAddr(t)
	raw, err := json.Marshal(map[string]any{
		"admin":   map[string]any{"disabled": true, "config": map[string]any{"persist": false}},
		"storage": map[string]any{"module": "file_system", "root": t.TempDir()},
		"logging": map[string]any{"logs": map[string]any{"default": map[string]any{"level": "ERROR"}}},
		"apps": map[string]any{
			"apx_trace": map[string]any{"event_sink": map[string]any{"url": sink.URL, "auth_env_var": "APX_TRACE_E2E_KEY"}},
			"http": map[string]any{
				"grace_period": "1s",
				"servers": map[string]any{"edge": map[string]any{
					"listen":          []string{addr},
					"automatic_https": map[string]any{"disable": true},
					"routes": []any{map[string]any{"handle": []any{
						map[string]any{"handler": "apx_trace"},
						map[string]any{"handler": "subroute", "routes": []any{
							route("/socket", upgradeEchoUpstream(t)),
							route("/events", sse),
						}},
					}}},
				}},
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, caddy.Load(raw, true))
	t.Cleanup(func() { _ = caddy.Stop() })

	wsTok := validTokenFor(t, secret, TokenPayload{DebugRequestID: "dr-e2e-ws", VhostID: 7})
	sseTok := validTokenFor(t, secret, TokenPayload{DebugRequestID: "dr-e2e-sse", VhostID: 7})
	t.Run("WebSocket", func(t *testing.T) { exchangeWebSocketFrames(t, addr, "/socket", wsTok) })
	t.Run("SSE", func(t *testing.T) {
		readSSE(t, &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}, "http://"+addr+"/events", "HTTP/1.1", sseTok, release, stalled)
	})
	if t.Failed() {
		return
	}

	var socket, events map[string]any
	require.Eventually(t, func() bool {
		for _, e := range sunk() {
			if e.event.Type != EventClusterResponse {
				continue
			}
			switch e.token {
			case wsTok:
				socket = e.event.Payload
			case sseTok:
				events = e.event.Payload
			}
		}
		return socket != nil && events != nil
	}, 10*time.Second, 50*time.Millisecond, "both traced requests must reach the sink as cluster_response events")
	require.Equal(t, true, socket["hijacked"])
	require.EqualValues(t, http.StatusOK, events["status"])
	require.GreaterOrEqual(t, events["bytes_written"], float64(len(sseBody)))
}

type sunkEvent struct {
	event Event
	token string
}

// traceEventSink records the events apx_trace posts, keyed by trace token.
func traceEventSink(t *testing.T) (*httptest.Server, func() []sunkEvent) {
	t.Helper()
	var (
		mu  sync.Mutex
		got []sunkEvent
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var e Event
		if err := json.NewDecoder(r.Body).Decode(&e); err == nil {
			mu.Lock()
			got = append(got, sunkEvent{event: e, token: r.Header.Get("apx-trace-token")})
			mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []sunkEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]sunkEvent(nil), got...)
	}
}

const sseBody = "data: one\n\ndata: two\n\n"

// sseUpstream sends one event, flushes, and holds the stream open until
// release closes or 5s pass; stalled reports the latter.
func sseUpstream(t *testing.T) (*httptest.Server, chan struct{}, *atomic.Bool) {
	t.Helper()
	release := make(chan struct{})
	stalled := new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: one\n\n")
		_ = http.NewResponseController(w).Flush()
		select {
		case <-release:
		case <-time.After(5 * time.Second):
			stalled.Store(true)
		}
		_, _ = io.WriteString(w, "data: two\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, release, stalled
}

// readSSE sends a traced request, asserts the first event arrives while the
// upstream still holds the stream open, then releases it and reads the rest.
func readSSE(t *testing.T, client *http.Client, url, proto, token string, release chan struct{}, stalled *atomic.Bool) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	req.Header.Set("X-APX-Debug-Trace", token)
	start := time.Now()
	res, err := client.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, proto, res.Proto)
	br := bufio.NewReader(res.Body)
	first, err := br.ReadString('\n')
	firstAt := time.Since(start)
	close(release)
	require.NoError(t, err)
	require.Equal(t, "data: one\n", first)
	require.False(t, stalled.Load(), "the first event arrived after %s, only once the upstream stopped waiting: no flush got through", firstAt)
	rest, err := io.ReadAll(br)
	require.NoError(t, err)
	require.Equal(t, sseBody, first+string(rest))
}

// upgradeEchoUpstream completes a WebSocket handshake and then echoes raw
// bytes until the client half-closes.
func upgradeEchoUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "upgrade required", http.StatusUpgradeRequired)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(brw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
			websocketAccept(r.Header.Get("Sec-WebSocket-Key")))
		if brw.Flush() != nil {
			return
		}
		_, _ = io.Copy(conn, brw.Reader)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// exchangeWebSocketFrames upgrades a raw, traced connection, round-trips
// three messages through the tunnel and closes it.
func exchangeWebSocketFrames(t *testing.T, addr, path, token string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))

	const key = "dGhlIHNhbXBsZSBub25jZQ=="
	_, err = fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: customer.example\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nX-APX-Debug-Trace: %s\r\n\r\n", path, key, token)
	require.NoError(t, err)
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusSwitchingProtocols, res.StatusCode)
	require.Equal(t, websocketAccept(key), res.Header.Get("Sec-WebSocket-Accept"))

	for i := range 3 {
		msg := fmt.Sprintf("frame %d\n", i)
		_, err := io.WriteString(conn, msg)
		require.NoError(t, err)
		got := make([]byte, len(msg))
		_, err = io.ReadFull(br, got)
		require.NoError(t, err)
		require.Equal(t, msg, string(got), "the tunnel must relay both ways after the 101")
	}
	require.NoError(t, conn.Close())
}

// caddyProxy provisions Caddy's reverse_proxy against upstream.
func caddyProxy(t *testing.T, upstream *httptest.Server) *reverseproxy.Handler {
	t.Helper()
	ctx, err := caddy.ProvisionContext(&caddy.Config{Admin: &caddy.AdminConfig{Disabled: true}})
	require.NoError(t, err)
	ctx, cancel := caddy.NewContext(ctx)
	t.Cleanup(cancel)
	transport := &reverseproxy.HTTPTransport{}
	require.NoError(t, transport.Provision(ctx))
	transport.Transport.Proxy = nil
	t.Cleanup(func() { _ = transport.Cleanup() })
	proxy := &reverseproxy.Handler{
		Transport: transport,
		Upstreams: reverseproxy.UpstreamPool{&reverseproxy.Upstream{Dial: upstream.Listener.Addr().String()}},
	}
	require.NoError(t, proxy.Provision(ctx))
	t.Cleanup(func() { _ = proxy.Cleanup() })
	return proxy
}

// traceEdge serves each request the way a Caddy 2.11.6+ server hands it to
// a route: under an Unwrap-only writer, prepared by Caddy, through the trace
// handler and then proxy. done receives the handler's result.
func traceEdge(t *testing.T, app *fakeTraceApp, proxy *reverseproxy.Handler, h2 bool) (*httptest.Server, <-chan error) {
	t.Helper()
	h := &TraceHandler{headerName: "X-APX-Debug-Trace", app: app, redactor: DefaultRedactor()}
	done := make(chan error, 4)
	edge := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w = &unwrapOnlyWriter{w}
		r = caddyhttp.PrepareRequest(r, caddy.NewReplacer(), w, &caddyhttp.Server{})
		done <- h.ServeHTTP(w, r, caddyhttp.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
			return proxy.ServeHTTP(w, r, nextHandlerOK())
		}))
	}))
	if h2 {
		edge.EnableHTTP2 = true
		edge.StartTLS()
	} else {
		edge.Start()
	}
	t.Cleanup(edge.Close)
	return edge, done
}

func waitHandled(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the trace handler did not return")
	}
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}
