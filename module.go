package apxtrace

import (
	"github.com/caddyserver/caddy/v2"
)

func init() {
	caddy.RegisterModule(&TraceHandler{})
	caddy.RegisterModule(&MarkHandler{})
	caddy.RegisterModule(&TraceTransport{})
}
