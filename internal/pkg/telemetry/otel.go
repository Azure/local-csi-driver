// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package telemetry

import (
	"context"
	"fmt"
	"time"

	prombridge "go.opentelemetry.io/contrib/bridges/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	"k8s.io/component-base/tracing"
	tracingv1 "k8s.io/component-base/tracing/api/v1"
)

type Provider interface {
	TraceProvider() tracing.TracerProvider
	MeterProvider() *metric.MeterProvider
}

type OtelProviders struct {
	tp tracing.TracerProvider
	mp *metric.MeterProvider
}

func New(ctx context.Context, opts ...Option) (*OtelProviders, error) {
	cfg := newConfig(opts)

	mp, err := InitMetrics(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize metrics: %w", err)
	}

	tp, err := InitTracing(ctx, cfg)
	if err != nil {
		if mp != nil {
			_ = mp.Shutdown(ctx)
		}
		return nil, fmt.Errorf("failed to initialize tracing: %w", err)
	}

	return &OtelProviders{
		tp: tp,
		mp: mp,
	}, nil
}

// InitTracing initializes the OpenTelemetry tracing provider. It should be called
// before traces are used.
func InitTracing(ctx context.Context, cfg config) (tracing.TracerProvider, error) {
	var otlpCfg *tracingv1.TracingConfiguration
	var resOpts []resource.Option
	if cfg.endpoint != "" && cfg.traceSampleRate > 0 {
		otlpCfg = &tracingv1.TracingConfiguration{
			Endpoint:               &cfg.endpoint,
			SamplingRatePerMillion: &cfg.traceSampleRate,
		}
		resOpts = NewResourceOptions(cfg)
	}

	// Use the trace provider from component-base so it can also be used with
	// Kubernetes components that support tracing. nil otlpCfg means that
	// the tracing provider will be a noop provider.
	tp, err := tracing.NewProvider(ctx, otlpCfg, []otlptracegrpc.Option{}, resOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create tracer provider: %w", err)
	}

	// Set the global tracer provider to the one we just created.
	otel.SetTracerProvider(tp)

	return tp, nil
}

// InitMetrics initializes the OpenTelemetry metrics provider. It should be
// called before metrics are used.
//
// Only the prometheus exporter is currently supported. The OTLP exporter should
// work but it's not been tested.
func InitMetrics(ctx context.Context, cfg config) (*metric.MeterProvider, error) {
	resource, err := resource.New(ctx, NewResourceOptions(cfg)...)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}
	opts := []metric.Option{
		metric.WithResource(resource),
	}

	// The prometheus bridge is used to read metrics from the promethus gatherer
	// (used by controller-runtime), and export them to the OTLP exporter.
	bridge := prombridge.NewMetricProducer(
		prombridge.WithGatherer(cfg.prometheus),
	)

	switch {
	case cfg.exporters[Console]:
		exp, err := stdoutmetric.New(stdoutmetric.WithTemporalitySelector(func(_ metric.InstrumentKind) metricdata.Temporality { return metricdata.DeltaTemporality }))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize console exporter: %w", err)
		}
		opts = append(opts, metric.WithReader(metric.NewPeriodicReader(exp, metric.WithProducer(bridge))))
	case cfg.exporters[OTLP]:
		exp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure(), otlpmetricgrpc.WithEndpoint(cfg.endpoint))
		if err != nil {
			return nil, fmt.Errorf("failed to initialize OTLP gRPC exporter: %w", err)
		}
		opts = append(opts, metric.WithReader(metric.NewPeriodicReader(exp, metric.WithProducer(bridge))))
	case cfg.exporters[Prometheus]:
		exp, err := prometheus.New(prometheus.WithRegisterer(cfg.prometheus), prometheus.WithoutTargetInfo())
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Prometheus exporter: %w", err)
		}
		opts = append(opts, metric.WithReader(exp))
	}

	mp := metric.NewMeterProvider(opts...)

	// Set the global meter provider to the one we just created.
	otel.SetMeterProvider(mp)

	return mp, nil
}

// TraceProvider returns the OpenTelemetry TracerProvider. It should be used to
// create tracers for the application.
func (p *OtelProviders) TraceProvider() tracing.TracerProvider {
	return p.tp
}

// MeterProvider returns the OpenTelemetry MeterProvider. It should be used to
// create meters for the application.
func (p *OtelProviders) MeterProvider() *metric.MeterProvider {
	return p.mp
}

// Start starts the OpenTelemetry providers and runs any shutdown tasks when the
// context is closed.
func (p *OtelProviders) Start(ctx context.Context) error {
	// Wait for context to be closed, then shutdown gracefully.
	<-ctx.Done()

	// New context since the original has ended.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if p.mp != nil {
		if err := p.mp.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown meter provider: %w", err)
		}
	}
	if p.tp != nil {
		if err := p.tp.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown trace provider: %w", err)
		}
	}
	return nil
}

// NeedLeaderElection returns false since the telemetry providers should always
// be available.
func (p *OtelProviders) NeedLeaderElection() bool {
	return false
}

// NewNoopTracerProvider creates a new no-op tracing provider.
func NewNoopTracerProvider() tracing.TracerProvider {
	return tracing.NewNoopTracerProvider()
}
