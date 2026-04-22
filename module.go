package apxtrace

import (
	"github.com/caddyserver/caddy/v2"
)

func init() {
	caddy.RegisterModule(&TraceHandler{})
	caddy.RegisterModule(&MarkHandler{})
	// Task 1.10 will register TraceTransport here.
}
