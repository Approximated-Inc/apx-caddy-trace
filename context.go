package apxtrace

import (
	"context"
	"time"
)

type ctxKey struct{}

// TraceContext travels on the request context for the duration of a traced
// request. Only present when a valid trace token was verified by HMAC.
//
// Not safe for concurrent mutation across goroutines: the trace pipeline
// assumes a single-threaded handler chain per request.
type TraceContext struct {
	Token          string
	DebugRequestID string
	VhostID        int64
	RequestID      string
	StartTime      time.Time
	App            AppRef
	LastSnapshot   RequestSnapshot
}

func withTrace(ctx context.Context, tc *TraceContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, tc)
}

// FromContext returns the active TraceContext if present, else nil.
func FromContext(ctx context.Context) *TraceContext {
	tc, _ := ctx.Value(ctxKey{}).(*TraceContext)
	return tc
}
