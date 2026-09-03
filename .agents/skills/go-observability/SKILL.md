---
name: go-observability
description: "Go observability with OpenTelemetry covering distributed tracing, metrics collection, structured logging with slog, and alerting strategies. Triggers on: go observability, go tracing, go metrics, go logging, go opentelemetry."
user-invocable: true
---

# Go Observability and OpenTelemetry

## OpenTelemetry Core Principles

### Three Pillars
  - Use **OpenTelemetry** for:
  - Distributed tracing
  - Metrics collection
  - Structured logging

### Context Propagation
- Always attach `context.Context` to spans, logs, and metric exports.
- Propagate context across all service boundaries:
  - HTTP requests
  - gRPC calls
  - Database operations
  - External API calls

### Telemetry Export
- Export data to **OTLP** exposed apis, e.g Otel Collector.
- Configure appropriate exporters for your observability backend.

## Distributed Tracing

### Tracer Setup
- Use **otel.Tracer** for creating spans.
- Start and propagate tracing **spans** across all service boundaries.

### Span Management
```go
ctx, span := tracer.Start(ctx, "operation-name")
defer span.End()

// Record important attributes
span.SetAttributes(
    attribute.String("user.id", userID),
    attribute.String("request.param", param),
)

// Record errors
if err != nil {
    span.RecordError(err)
    span.SetStatus(codes.Error, err.Error())
}
```

### Tracing Best Practices
- Trace all **incoming requests**.
- Propagate context through internal and external calls.
- Use **middleware** to instrument HTTP and gRPC endpoints automatically.
- Annotate slow, critical, or error-prone paths with **custom spans**.

### Span Attributes
Record important attributes:
- Request parameters
- User identifiers
- Error messages
- Business-relevant data
- Resource identifiers

## Metrics Collection

### Meter Setup
- Use **otel.Meter** for collecting metrics.
- Define appropriate metric types:
  - Counters
  - Gauges
  - Histograms
- Avoid high-cardinality labels, e.g user IDs, session IDs, etc.

### Key Metrics
Monitor application health via:
- **Request latency**
- **Throughput** (requests per second)
- **Error rate** (5xx responses)
- **Resource usage** (CPU, memory, goroutines)

### Service Level Indicators (SLIs)
- Define **SLIs** for your service (e.g., request latency < 300ms).
- Track SLIs with **Prometheus/Grafana** dashboards.
- Set up alerts based on SLI thresholds.

## Structured Logging

### Logging Standards
- Use **slog** package for all logging.
- Emit **JSON-formatted logs** for observability tool ingestion.
- Use structured fields: `slog.String()`, `slog.Any()`.

### Log Correlation
- Use **log correlation** by injecting trace IDs into logs.
- Include unique **request IDs** in all logs.
- Attach context to log entries for correlation.

### Log Levels
Use **log levels** appropriately:
- `info`: Normal operational messages
- `warn`: Warning conditions that should be investigated
- `error`: Error conditions that require attention

### Log Content
Include in logs:
- Trace ID and span ID
- Request ID
- User ID (when applicable)
- Error details (with stack traces when helpful)
- Relevant business context

## Alerting Strategy apply only when asked

### Alert Conditions
Define alerts for:
- High 5xx error rates
- Database connection errors
- Redis/cache timeouts
- Circuit breaker trips
- SLI violations

### Alert Quality
- Use a robust alerting pipeline.
- Avoid alert fatigue with proper thresholds.
- Include actionable information in alerts.
- Route alerts to appropriate teams.

## Performance Considerations

### Cardinality Control
- Avoid excessive **cardinality** in labels and traces.
- Use bounded label values.
- Keep observability overhead minimal.

### Sampling
- Consider trace sampling for high-traffic services.
- Use appropriate sampling strategies.
- Ensure critical paths are always traced.

## Middleware Integration

### Automatic Instrumentation
- Use middleware to instrument endpoints automatically.
- Instrument at framework/router level for consistency.
- Ensure all requests are traced.

## Best Practices

### Implementation
1. Always propagate context through function calls.
2. Start spans for all significant operations.
3. Record errors and important attributes in spans.
4. Include trace context in all logs.
5. Export telemetry to centralized collectors.

### Monitoring
1. Monitor key application metrics continuously.
2. Set up dashboards for operational visibility.
3. Define and track SLIs for service health.
4. Alert on anomalies and threshold violations.

### Optimization
1. Keep observability overhead low.
2. Use sampling for high-volume traces.
3. Avoid high-cardinality labels.
4. Profile observability code paths.
