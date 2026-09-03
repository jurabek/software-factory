---
name: go-testing
description: "Go testing best practices covering unit testing with testify, table-driven patterns, mocking with mockgen, integration testing with TestContainers, and HTTP handler testing. Triggers on: go test, go testing, go mock, go integration test."
user-invocable: true
---

# Go Testing Best Practices

## Unit Testing

### Testing Framework
- Use **testify** for unit testing and assertions.
- Write tests using **table-driven patterns**.
- Enable **parallel execution** where possible.
- Test all possible cases, mock external dependencies

### Test Structure
```go
func TestSomething(t *testing.T) {

    tests := []struct {
        name    string
        input   string
        want    string
        onMock  func(mockA *MockA, mockB *MockB)
        wantErr bool
    }{
        {name: "valid input", input: "test", want: "result", wantErr: false},
        {name: "invalid input", input: "", want: "", wantErr: true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()

            ctrl := gomock.NewController(t)
	        defer ctrl.Finish()

            if tt.onMock != nil {
                mockA := NewMockA(ctrl)
		        mockB := NewMockB(ctrl)
                tt.onMock(mockA, mockB)
            }
            // test logic
        })
    }
}
```

### Mocking
- **Mock external interfaces** using mockgen:
  ```go
  //go:generate go run go.uber.org/mock/mockgen -source=$GOFILE
  ```
- Generate mocks for all external dependencies.
- Keep mocks in sync with interface changes.

### Coverage
- Ensure **test coverage** for every exported function.
- Use behavioral checks, not just coverage metrics.
- Run `go test -cover` to measure coverage.
- Aim for high coverage with meaningful tests.

## Integration Testing

### Location and Structure
- Store integration tests in: `project_folder/tests/integration/feature_package/`
- Use **testify/suite** for test organization.
- Use **TestContainers** for running dependencies:
  - PostgreSQL
  - Kafka
  - Redis
  - Other services

### Integration Test Example
```go
type IntegrationTestSuite struct {
    suite.Suite
    db        *sql.DB
    container testcontainers.Container
}

func (s *IntegrationTestSuite) SetupSuite() {
    // Setup TestContainers
}

func (s *IntegrationTestSuite) TearDownSuite() {
    // Cleanup
}

func TestIntegrationSuite(t *testing.T) {
    suite.Run(t, new(IntegrationTestSuite))
}
```

## HTTP Handler Testing

### Testing Approach
1. **Call the function** to get the `http.Handler`:
   - Pass in all required dependencies (this is a feature).

2. **Call ServeHTTP** on the handler:
   - Use a real `http.Request`.
   - Use `httptest.ResponseRecorder` for capturing response.

3. **Make assertions** about the response:
   - Check the status code.
   - Decode the body and verify contents.
   - Check important headers.

### Example
```go
func TestHandler(t *testing.T) {
    // Setup
    handler := NewHandler(mockService)
    req := httptest.NewRequest(http.MethodGet, "/foo/123", nil)
    rec := httptest.NewRecorder()

    // Execute
    handler.ServeHTTP(rec, req)

    // Assert
    assert.Equal(t, http.StatusOK, rec.Code)

    var response Response
    err := json.NewDecoder(rec.Body).Decode(&response)
    assert.NoError(t, err)
    assert.Equal(t, "expected", response.Field)
}
```

## Test Organization

### Separation of Concerns
- Separate **fast unit tests** from slower integration and E2E tests.
- Use build tags to separate test types if needed.
- Keep test files close to the code they test.

### Test Utilities
- Store shared test utilities in `test/` directory.
- Create test helpers for common setup/teardown.
- Keep test utilities DRY and reusable.

## Best Practices

### Test Quality
- Write tests that verify **behavior**, not implementation.
- Test edge cases and error conditions.
- Use descriptive test names that explain what's being tested.
- Keep tests simple and focused.

### Database Testing
- Use **sqlx** or **standard sql package** for SQL operations.
- Use **goose** for migrations in tests.
- Reset database state between tests.
- Use transactions for test isolation when possible.

### Performance Testing
- Write **benchmarks** for performance-critical code.
- Use `go test -bench` to run benchmarks.
- Track benchmark results over time to catch regressions.
