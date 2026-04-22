package apxtrace

import (
	"context"
	"time"
)

type ctxKey struct{}

// TraceContext travels on the request context for the duration of a traced
// request. Only present when a valid trace token is confirmed in Redis.
type TraceContext struct {
	Token         string
	VhostID       string
	ProxyServerID string
	RequestID     string
	StartTime     time.Time
	// Emitter is typed `any` for now; narrowed to *Emitter in Task 1.7.
	Emitter      any
	LastSnapshot RequestSnapshot
}

func withTrace(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// FromContext returns the active TraceContext if present, else nil.
func FromContext(ctx context.Context) *TraceContext {
	tc, _ := ctx.Value(ctxKey{}).(*TraceContext)
	return tc
}
