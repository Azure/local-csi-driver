// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package enforceEphemeral

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// LatencyMetric observes latency.
type LatencyMetric interface {
	Observe(latency time.Duration)
}

// ResponseMetric counts handler responses.
type ResponseMetric interface {
	Increment(op opType, resp admission.Response)
}

var (
	// HandlerDuration is the latency metric that measures how long it takes to
	// handle a request.
	HandlerDuration LatencyMetric = &latencyAdapter{m: handlerLatencyHistogram}

	// Errors counts handler responses.
	HandlerResponse ResponseMetric = &responseAdapter{m: handlerResultCounter}

	// registerMetricsOnce keeps track of metrics registration.
	registerMetricsOnce sync.Once
)

var (
	handlerLatencyHistogram = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "pvc_duration_seconds",
			Help:    "Distribution of the length of time to handle pvc requests.",
			Buckets: prometheus.DefBuckets,
		},
	)

	handlerResultCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pvc_total",
			Help: "Number of processed pvc requests, partitioned by operation and whether it was allowed.",
		},
		[]string{"operation", "allowed"},
	)
)

// RegisterMetrics ensures that the package metrics are registered.
func RegisterMetrics() {
	registerMetricsOnce.Do(func() {
		metrics.Registry.MustRegister(handlerLatencyHistogram)
		metrics.Registry.MustRegister(handlerResultCounter)
	})
}

type latencyAdapter struct {
	m prometheus.Histogram
}

func (l *latencyAdapter) Observe(latency time.Duration) {
	l.m.Observe(latency.Seconds())
}

type responseAdapter struct {
	m *prometheus.CounterVec
}

func (r *responseAdapter) Increment(op opType, resp admission.Response) {
	r.m.WithLabelValues(string(op), fmt.Sprint(resp.Allowed)).Inc()
}
