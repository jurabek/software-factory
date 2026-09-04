---
name: go-best-practices
description: "Go readability, package, API, and concurrency practices from Twelve Go Best Practices. Always applies to every Go task: writing, changing, reviewing, debugging, or testing Go code."
---

# Go Best Practices

Apply these principles to every Go change. Preserve repository-specific conventions when they are stricter or more current.

## Readability

- Handle errors and invalid conditions first, then keep the successful path minimally indented.
- Remove repetition when a small local helper, adapter, or utility type makes the code clearer.
- Put the package's significant types and primary behavior before helpers and minor implementation details.
- Use the shortest name that remains self-explanatory in its package context.
- Document packages and exported identifiers with accurate Go documentation when their purpose is not already clear from the repository's established documentation policy.
- Split very long files by cohesive responsibility. Keep production code and tests in separate files.

## Packages and APIs

- Keep reusable packages buildable and testable through normal Go tooling.
- Accept the narrowest capability the function needs. Prefer a small consumer-defined interface over a concrete dependency when multiple implementations or test substitution provide real value.
- Keep packages independent: define boundary interfaces at the consuming package instead of importing an implementation solely for its concrete type.
- Expose synchronous operations by default. Let callers choose concurrency unless the abstraction inherently owns asynchronous execution.

## Concurrency

- Give every goroutine clear ownership, a completion path, and a cancellation or shutdown path.
- Use goroutines to serialize ownership of state when that is simpler than shared-memory synchronization.
- Prevent blocked sends after a caller returns. Use capacity only when the maximum number of results is known; otherwise make cancellation observable to senders.
- Verify that all goroutines can exit on success, error, cancellation, and early return.

## Completion Check

Before completing a Go task, inspect every changed Go path against the rules above. Run the repository's applicable formatting, tests, race checks, type checks, and build checks.

The downloaded source is [references/bestpractices.slide](references/bestpractices.slide). Use it when examples or the talk's original wording would materially clarify a decision. Original: https://go.dev/talks/2013/bestpractices.slide#34
