---
name: go-core-practices
description: "Go core development practices covering function design, error handling, dependency management, concurrency, tooling, and performance. Applies to all Go code generation."
user-invocable: true
---

# Go Core Development Practices

## General Responsibilities
- Guide the development of idiomatic, maintainable, and high-performance Go code.
- Enforce modular design and separation of concerns.
- Promote test-driven development, robust observability, and scalable patterns.

## Code Quality Standards

### Function Design
- Write **short, focused functions** with a single responsibility.
- Prioritize **readability, simplicity, and maintainability**.
- Keep functions small and easily testable.

### Error Handling
- Always **check and handle errors explicitly**.
- Use wrapped errors for traceability: `fmt.Errorf("context: %w", err)`.
- Never ignore errors; handle them appropriately at each level.

### Dependency Management
- Avoid **global state**; use constructor functions to inject dependencies.
- Always receive **interfaces** as dependencies (not concrete types).
- Define interfaces on the **consumer side** where applicable.
- Ensure all public functions interact with interfaces, not concrete types.

### Concurrency Best Practices
- Use **goroutines safely**; guard shared state with channels or sync primitives.
- Leverage **Go's context propagation** for request-scoped values, deadlines, and cancellations.
- Implement **goroutine cancellation** using context to avoid leaks and deadlocks.
- **Defer closing resources** and handle them carefully to avoid leaks.

### Design Principles
- Prefer **composition over inheritance**.
- Favor small, purpose-specific interfaces.
- Design for **change**: isolate business logic and minimize framework lock-in.
- Emphasize clear **boundaries** and **dependency inversion**.

## Tooling and Dependencies

### Standard Practices
- Prefer the **standard library** where feasible.
- Only rely on **stable, minimal third-party libraries**.
- Use **Go modules** for dependency management and reproducibility.
- Version-lock dependencies for deterministic builds.

### Code Quality Tools
- Use `gofumpt -l -w .` for code formatting.
- Use `golangci-lint run -v` for linting.
- Enforce naming consistency with `go fmt` and `goimports`.
- Integrate **linting, testing, and security checks** in CI pipelines.

### Logging
- Always use **slog** package for logging.
- Emit **JSON-formatted logs** for ingestion by observability tools.
- Use appropriate **log levels** (info, warn, error).
- Use structured logging: `slog.String()`, `slog.Any()`.
- Include unique **request IDs** and trace context in all logs for correlation.

## Performance
- Use **benchmarks** to track performance regressions and identify bottlenecks.
- Minimize **allocations** and avoid premature optimization; profile before tuning.
- Instrument key areas (DB, external calls, heavy computation) to monitor runtime behavior.

## Key Conventions
1. Always receive interfaces as a dependency.
2. Prioritize **readability, simplicity, and maintainability**.
3. Design for **change**: isolate business logic and minimize framework lock-in.
4. Create small interfaces where it is used, not where it implemented unless it is necessary.
5. Ensure all behavior is **observable, testable, and documented**.
6. **Automate workflows** for testing, building, and deployment.
