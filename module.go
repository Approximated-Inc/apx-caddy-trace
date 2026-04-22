package apxtrace

import (
	"github.com/caddyserver/caddy/v2"
)

func init() {
	caddy.RegisterModule(&TraceHandler{})
	// Registrations for MarkHandler and TraceTransport will be added by
	// Tasks 1.9 and 1.10. Leaving the init isolated here so each task can
	// append without conflicts.
}
