---
name: go-http-api
description: "Go HTTP server and API development using standard library, route organization, JSON encoding/decoding with generics, validation, middleware, and security patterns. Triggers on: go http, go api, go handler, go middleware, go server."
user-invocable: true
---

# Go HTTP Server and API Development

## HTTP Server Best Practices

### Standard Library Usage
- Always use **standard lib http package** for HTTP handlers and server setup.
- Use **latest mux patterns** from Go >= v1.22.
- Avoid unnecessary frameworks when standard library suffices.

### Route Organization
Use helper **addRoutesFeature** functions for adding handlers into specific routes:

```go
func addFooRoute(mux *http.ServeMux) {
    fooHandler := &FooHandler{}
    mux.HandleFunc("/foo/{id}", fooHandler.GetFoo)
}
```

## Encoding and Decoding

### Centralized JSON Handling
Handle decoding/encoding in one place using generics:

```go
func encode[T any](w http.ResponseWriter, r *http.Request, status int, v T) error {
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(status)
    if err := json.NewEncoder(w).Encode(v); err != nil {
        return fmt.Errorf("encode json: %w", err)
    }
    return nil
}

func decode[T any](r *http.Request) (T, error) {
    var v T
    if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
        return v, fmt.Errorf("decode json: %w", err)
    }
    return v, nil
}

// Usage:
err := encode(w, r, http.StatusOK, obj)
decoded, err := decode[CreateSomethingRequest](r)
```

## Request Validation

### Validator Interface
All request types should implement the Validator interface:

```go
// Validator is an object that can be validated.
type Validator interface {
    // Valid checks the object and returns any problems.
    // If len(problems) == 0 then the object is valid.
    // Field names are used as keys, with human-readable
    // explanations as values.
    Valid(ctx context.Context) (problems map[string]string)
}
```

## Middleware Patterns

### Adapter Pattern
Use the Adapter pattern for middlewares:

```go
// Authorization middleware
func adminOnly(h http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        if !currentUser(r).IsAdmin {
            http.NotFound(w, r)
            return
        }
        h.ServeHTTP(w, r)
    })
}
```

### Error Handling
- Implement centralized error handling using handler adapters.
    Example:
    ```go
    // Handler that returns errors
    func fooHandler(w http.ResponseWriter, r *http.Request) error {
        if err := bar(); err != nil {
            return fmt.Errorf("handling bar failed: %w", err)
        }
        return nil
    }

    // Error handler adapter
    func ErrorHandler(f func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
        return func(w http.ResponseWriter, r *http.Request) {
            err := f(w, r)
            if err != nil {
                statusCode, message := mapFooErrors(err)
                respondWithError(w, r, statusCode, message)
            }
        }
    }

    // Usage:
    mux.HandleFunc("/foo", ErrorHandler(fooHandler))
    ```

## Open API Specs
- Always maintain open api specs for exposed API's
- Be concise about what fields are exposed and what request parameters are required.

## Security Best Practices

### Input Validation
- Apply **input validation and sanitization** rigorously.
- Especially important for inputs from external sources.
- Use the Validator interface pattern consistently.

### Secure Defaults
- Use secure defaults for **JWT, cookies**, and configuration settings.
- Isolate sensitive operations with clear **permission boundaries**.
- Never trust client input without validation.

## Resilience Patterns

### External Call Protection
- Implement **retries, exponential backoff, and timeouts** on all external calls.
- Use **circuit breakers and rate limiting** for service protection.
- Consider implementing **distributed rate-limiting** (e.g., using Redis).

### Context Propagation
- Always propagate context through request lifecycle.
- Use context for cancellation, timeouts, and request-scoped values.
- Include trace IDs and request metadata in context.
