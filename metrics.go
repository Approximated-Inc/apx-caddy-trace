package apxtrace

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// metricRequestsTotal counts outer-wrapper invocations by outcome.
// Labels:
//   result = "no_header" | "invalid_token" | "redis_error" | "active"
var metricRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "apx_trace_requests_total",
		Help: "apx-caddy-trace outer wrapper invocations by result",
	},
	[]string{"result"},
)

// metricEventsEmittedTotal counts events emitted to Redis streams.
var metricEventsEmittedTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "apx_trace_events_emitted_total",
		Help: "apx-caddy-trace events emitted to Redis streams",
	},
	[]string{"type"},
)

// metricEventsDropped counts events dropped due to full per-session buffer.
var metricEventsDropped = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "apx_trace_events_dropped_total",
		Help: "apx-caddy-trace events dropped by bounded buffer overflow",
	},
)

// metricRedisErrors counts Redis errors observed by the plugin.
var metricRedisErrors = promauto.NewCounter(
	prometheus.CounterOpts{
		Name: "apx_trace_redis_errors_total",
		Help: "apx-caddy-trace Redis operation errors",
	},
)

// metricRedisWriteDuration observes XADD latency in seconds.
var metricRedisWriteDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "apx_trace_redis_write_duration_seconds",
		Help:    "apx-caddy-trace Redis XADD latency",
		Buckets: prometheus.ExponentialBuckets(0.0005, 2, 10),
	},
)
