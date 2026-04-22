package apxtrace

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// RedisConfig is an inline Redis configuration passed via the handler's JSON.
// When present, it takes precedence over env-based configuration.
type RedisConfig struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	DB       int    `json:"db,omitempty"`
	Password string `json:"password,omitempty"`
	TLS      bool   `json:"tls,omitempty"`
}

// armer is the subset of redis needed to check arming keys.
type armer interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	streamer
}

// TraceHandler is the outer wrapper installed once per server.
// Dormant unless X-APX-Debug-Trace header is present with a valid token.
type TraceHandler struct {
	Redis      *RedisConfig `json:"redis,omitempty"`
	HeaderName string       `json:"header_name,omitempty"`

	logger       *zap.Logger
	headerName   string
	arm          armer
	emitterMaker func(token string) *Emitter
	redactor     *Redactor

	mu       sync.Mutex
	emitters map[string]*Emitter
}

// CaddyModule returns the Caddy module information.
func (*TraceHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.apx_trace",
		New: func() caddy.Module { return new(TraceHandler) },
	}
}

// Provision initializes the handler with a Redis client + redactor.
func (h *TraceHandler) Provision(ctx caddy.Context) error {
	h.logger = ctx.Logger()
	h.headerName = h.resolveHeaderName()
	h.redactor = DefaultRedactor()
	h.emitters = make(map[string]*Emitter)

	if h.arm == nil {
		opts, err := h.resolveRedisOpts()
		if err != nil {
			return fmt.Errorf("apx_trace provision: %w", err)
		}
		h.arm = redis.NewClient(opts)
	}
	if h.emitterMaker == nil {
		h.emitterMaker = func(token string) *Emitter {
			return NewEmitter(h.arm, streamKeyFor(token), 256)
		}
	}
	return nil
}

func (h *TraceHandler) resolveHeaderName() string {
	if h.HeaderName != "" {
		return h.HeaderName
	}
	return defaultStringEnv("APX_TRACE_HEADER", "X-APX-Debug-Trace")
}

func (h *TraceHandler) resolveRedisOpts() (*redis.Options, error) {
	if h.Redis != nil && h.Redis.Host != "" {
		opts := &redis.Options{
			Addr:         fmt.Sprintf("%s:%d", h.Redis.Host, h.Redis.Port),
			Password:     h.Redis.Password,
			DB:           h.Redis.DB,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
			PoolSize:     10,
		}
		if h.Redis.TLS {
			opts.TLSConfig = &tls.Config{}
		}
		return opts, nil
	}
	return RedisOptsFromEnv()
}

// UnmarshalCaddyfile parses an empty block.
func (h *TraceHandler) UnmarshalCaddyfile(d *caddyfile.Dispenser) error { return nil }

var tokenFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{32,64}$`)

// ServeHTTP is the hot path. Must be cheap when no trace header is present.
func (h *TraceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	token := r.Header.Get(h.headerName)
	if token == "" {
		metricRequestsTotal.WithLabelValues("no_header").Inc()
		return next.ServeHTTP(w, r)
	}
	if !tokenFormat.MatchString(token) {
		metricRequestsTotal.WithLabelValues("invalid_token").Inc()
		return next.ServeHTTP(w, r)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
	defer cancel()

	raw, err := h.arm.Get(ctx, armKeyFor(token)).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			metricRequestsTotal.WithLabelValues("invalid_token").Inc()
			return next.ServeHTTP(w, r)
		}
		metricRedisErrors.Inc()
		metricRequestsTotal.WithLabelValues("redis_error").Inc()
		return next.ServeHTTP(w, r)
	}

	var armPayload struct {
		VhostID       string `json:"virtual_host_id"`
		ProxyServerID string `json:"proxy_server_id"`
	}
	if err := json.Unmarshal([]byte(raw), &armPayload); err != nil {
		metricRequestsTotal.WithLabelValues("invalid_token").Inc()
		return next.ServeHTTP(w, r)
	}

	metricRequestsTotal.WithLabelValues("active").Inc()

	emitter := h.acquireEmitter(token)
	tc := &TraceContext{
		Token:         token,
		VhostID:       armPayload.VhostID,
		ProxyServerID: armPayload.ProxyServerID,
		RequestID:     uuid.NewString(),
		StartTime:     time.Now(),
		Emitter:       emitter,
		LastSnapshot:  Snapshot(r),
	}

	// cluster_received
	emitter.Emit(Event{
		Type:   EventClusterReceived,
		TsNs:   time.Now().UnixNano(),
		Source: SourceCluster,
		Payload: map[string]any{
			"method":     r.Method,
			"uri":        r.URL.RequestURI(),
			"host":       r.Host,
			"headers":    h.redactor.RedactHeaders(r.Header),
			"remote_ip":  r.RemoteAddr,
			"request_id": tc.RequestID,
		},
	})

	wrapped := &responseRecorder{ResponseWriter: w, status: 200}
	req := r.WithContext(withTrace(r.Context(), tc))

	var servErr error
	defer func() {
		total := time.Since(tc.StartTime)
		payload := map[string]any{
			"total_duration_ns": total.Nanoseconds(),
			"request_id":        tc.RequestID,
		}
		if wrapped.hijacked {
			payload["hijacked"] = true
		} else {
			payload["status"] = wrapped.status
			payload["response_headers"] = h.redactor.RedactHeaders(wrapped.Header())
			payload["bytes_written"] = wrapped.bytes
		}
		if servErr != nil {
			payload["error"] = servErr.Error()
		}
		rec := recover()
		if rec != nil {
			payload["panic"] = fmt.Sprintf("%v", rec)
		}
		emitter.Emit(Event{
			Type:    EventClusterResponse,
			TsNs:    time.Now().UnixNano(),
			Source:  SourceCluster,
			Payload: payload,
		})
		go h.releaseEmitter(token, 2*time.Second)

		// Re-panic so Caddy's server-level recovery can handle it as usual.
		if rec != nil {
			panic(rec)
		}
	}()

	servErr = next.ServeHTTP(wrapped, req)
	return servErr
}

func (h *TraceHandler) acquireEmitter(token string) *Emitter {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.emitters == nil {
		h.emitters = make(map[string]*Emitter)
	}
	if e, ok := h.emitters[token]; ok {
		return e
	}
	e := h.emitterMaker(token)
	h.emitters[token] = e
	return e
}

func (h *TraceHandler) releaseEmitter(token string, after time.Duration) {
	time.Sleep(after)
	h.mu.Lock()
	e, ok := h.emitters[token]
	if ok {
		delete(h.emitters, token)
	}
	h.mu.Unlock()
	if e != nil {
		e.Close()
	}
}

// responseRecorder captures status + bytes + headers while still writing to w.
type responseRecorder struct {
	http.ResponseWriter
	status   int
	bytes    int
	wrote    bool
	hijacked bool
}

func (rr *responseRecorder) WriteHeader(code int) {
	if rr.wrote {
		return
	}
	rr.status = code
	rr.wrote = true
	rr.ResponseWriter.WriteHeader(code)
}

func (rr *responseRecorder) Write(b []byte) (int, error) {
	if !rr.wrote {
		rr.wrote = true
	}
	n, err := rr.ResponseWriter.Write(b)
	rr.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController (Go 1.20+) and Caddy traverse the
// wrapper chain so inner capabilities stay reachable.
func (rr *responseRecorder) Unwrap() http.ResponseWriter { return rr.ResponseWriter }

// Flush delegates to the inner writer if it implements http.Flusher.
// Required for SSE and any streaming response.
func (rr *responseRecorder) Flush() {
	if f, ok := rr.ResponseWriter.(http.Flusher); ok {
		if !rr.wrote {
			rr.wrote = true
		}
		f.Flush()
	}
}

// Hijack delegates to the inner writer if it implements http.Hijacker.
// Required for WebSocket upgrades and any raw TCP takeover.
func (rr *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hj, ok := rr.ResponseWriter.(http.Hijacker); ok {
		rr.wrote = true
		rr.hijacked = true
		return hj.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Push delegates to the inner writer if it implements http.Pusher.
func (rr *responseRecorder) Push(target string, opts *http.PushOptions) error {
	if p, ok := rr.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

func armKeyFor(token string) string    { return "debug:trace:" + token }
func streamKeyFor(token string) string { return "debug:trace:" + token + ":events" }

func defaultStringEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

var (
	_ caddy.Provisioner           = (*TraceHandler)(nil)
	_ caddyfile.Unmarshaler       = (*TraceHandler)(nil)
	_ caddyhttp.MiddlewareHandler = (*TraceHandler)(nil)
)
