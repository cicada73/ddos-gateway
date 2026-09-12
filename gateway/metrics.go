package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Three metrics, deliberately kept simple - each one answers one question
// someone looking at the dashboard would actually ask:
//
//	gateway_requests_total          "how many requests, and what happened to them?"
//	gateway_request_duration_seconds "how fast is the gateway making that decision?"
//	gateway_anomaly_new_flags_total  "how many NEW anomalous identities have been caught?"
//
// promauto registers each metric with Prometheus's default registry
// automatically, which is what promhttp.Handler() (wired up in main.go)
// serves at /metrics.
var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total requests handled by the gateway, labeled by outcome and replica.",
	}, []string{"outcome", "replica"}) // outcome: allowed | rate_limited | anomaly_blocked

	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "Time taken by the gateway to decide + proxy a request, labeled by outcome.",
		Buckets: prometheus.DefBuckets,
	}, []string{"outcome"})

	// Counts only the request that TRIGGERED a new flag (not every
	// subsequent request blocked while that flag is active) - this is
	// "how many distinct anomalous identities have we caught", not
	// "how many requests did the anomaly layer block".
	anomalyNewFlagsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_anomaly_new_flags_total",
		Help: "Count of newly-triggered anomaly flags, labeled by reason.",
	}, []string{"reason"}) // reason: key_cycling | ip_cycling
)
