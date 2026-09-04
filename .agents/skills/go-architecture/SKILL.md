---
name: go-architecture
description: "Go architecture patterns including clean architecture, DDD, interface-driven development, and project structure guidelines. Triggers on: go project setup, go architecture, go project structure."
user-invocable: true
---

# Go Architecture and Project Structure

## Architecture Patterns

### Clean Architecture
- Structure code into layers:
  - **Handlers**: HTTP/gRPC endpoints
  - **Services/Use Cases**: Business logic
  - **Repositories/Data Access**: Data persistence
  - **Domain Models**: Core entities and types
- Keep logic decoupled from framework-specific code.

### Domain-Driven Design
- Use **domain-driven design** principles where applicable.
- Define clear domain boundaries and entities.
- Keep domain logic independent of infrastructure concerns.

### Interface-Driven Development
- Prioritize **interface-driven development** with explicit dependency injection.
- Define interfaces on the **consumer side** where applicable.
- Prefer **composition over inheritance**.
- Favor small, purpose-specific interfaces.
- Ensure that all public functions interact with interfaces, not concrete types.

## Project Structure Guidelines

### Standard Layout
Use a consistent project layout:

```
cmd/              # Application entrypoints
internal/         # Core application logic (not exposed externally)
pkg/              # Shared utilities and packages
configs/          # Configuration schemas and loading
test/             # Test utilities, mocks, and integration tests
```

### Configuration Management
- Store configuration in `configs/` directory.
- Use **github.com/kelseyhightower/envconfig** for loading environment variables into config structs.
- Follow simple, declarative configuration patterns.

### Code Organization
- Group code by **feature** when it improves clarity and cohesion.
- Keep logic decoupled from framework-specific code.
- Maintain clear boundaries between layers.
- Always avoid mutability, and use pure functions where needed.

## Documentation Standards

### Code Documentation
- Document public functions and packages with **GoDoc-style comments**. if asked
- Provide concise **READMEs** for services and libraries.
- Maintain `CONTRIBUTING.md` and `ARCHITECTURE.md` to guide team practices.

### Best Practices
- Keep documentation close to the code.
- Update documentation with code changes, if any.
- Include examples where helpful.
