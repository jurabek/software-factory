---
name: go-database
description: "Go database best practices covering sqlx, migrations with goose, repository pattern, transaction management with UnitOfWork, and query optimization. Triggers on: go database, go repository, go migration, go transaction."
user-invocable: true
---

# Go Database Best Practices

## Database Libraries

### SQL Operations
- Use **github.com/jmoiron/sqlx** for enhanced SQL operations. Check instruction on `Guide to jmoiron/sqlx library`
- Use **standard sql package** as foundation.
- Use always lower case SQL syntax for better readability. e.g `select`, `from`, `where` etc.
- Avoid SQL functions and triggers in favor of application logic.

## Database Migrations
- Use **goose** for creating and applying migrations e.g `goose create migration_name`
- Write both up and down migrations.
- Test migrations in development before production.
- Keep migrations small and focused.
- Never modify existing migrations after deployment.
- Use descriptive migration names.
- When writing migrations, avoid complex logic; prefer simple SQL statements, and do not create SQL functions or triggers.

## Repository Pattern
- Define repository interfaces where it used, only define used methods.
	Example
	```go
	// In service package
	type userGetter interface {
		GetUser(ctx context.Context, id string) (*User, error)
	}
	```

### Context Propagation
- Always pass `context.Context` to database operations.
- Use context for:
  - Query cancellation
  - Timeout enforcement
  - Tracing propagation
  - Transaction management

## Transaction Management
- Apply transaction management where ACID operation is needed.

### Transaction Pattern
- Example:
	```go
	type Transactioner struct {
		DB *sqlx.DB
	}

	func (tr *Transactioner) WithTx(ctx context.Context, opts *sql.TxOptions, fn func(s *sqlx.Tx) error) error {
		tx, err := tr.DB.BeginTxx(ctx, opts)
		if err != nil {
			return fmt.Errorf("begin transaction failed: %w", err)
		}

		defer func() {
			if p := recover(); p != nil {
				// a panic occurred, rollback and re-panic
				if rbErr := tx.Rollback(); rbErr != nil {
					fmt.Println("rollback err :%+v", err)
				}
				panic(p)
			}
		}()
		// we should pass nil into transaction beginner, to avoid starting new transaction within transaction
		err = fn(tx)
		if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return fmt.Errorf("transaction failed: %w rollback failed: %w", err, rbErr)
			}
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit failed: %w", err)
		}
		return nil
	}
	```

### Using transactional pattern with UnitOfWork
- Example:
	```go
	type UnitOfWork struct {
		Users UserRepository
		Orders OrderRepository
	}
	type WithdrawalUowFactory struct {
		tx     *database.Transactioner
	}
	func NewWithdrawalUowFactory(tx *database.Transactioner) *WithdrawalUowFactory {
		return &WithdrawalUowFactory{
			tx:     tx,
		}
	}
	func (f *WithdrawalUowFactory) Execute(ctx context.Context, uowFn func(*UnitOfWork) error) error {
		return f.tx.WithTx(ctx, nil, func(tx *sqlx.Tx) error {
			return uowFn(&UnitOfWork{
				Users:   NewUserRepository(tx),
				Orders:  NewOrderRepository(tx),
			})
		})
	}

	// on the service layer
	type withdrawalUowFactory interface {
		Execute(ctx context.Context, uowFn func(*UnitOfWork) error) error
	}

	type Service struct {
		uow withdrawalUowFactory
	}

	func (s *Service) SomeWork() {
		s.uow.Execute(ctx, func(uow UnitOfWork) error {
			// Get user with row lock
			user, err := uow.Users.GetByIDForUpdate(ctx, userID)
			if err != nil {
				return fmt.Errorf("failed to get user: %w", err)
			}

			// Get orders with row lock
			orders, err := uow.Orders.GetByIDForUpdate(ctx, orderId)
			if err != nil {
				return fmt.Errorf("failed to get order: %w", err)
			}
			return nil
		})
	}
	```

## Error Handling

### Database Errors
- Wrap database errors with context.
- Check for specific error types (e.g., `sql.ErrNoRows`).
- Provide meaningful error messages.

## Query Optimization

### Best Practices
- Use prepared statements for repeated queries.
- Implement proper indexing strategies.
- Use query parameters to prevent SQL injection.
- Avoid N+1 query problems.
- Use EXPLAIN ANALYZE to understand query performance.

### Efficient Queries
```go
// Good: Single query with JOIN and use StructScan by defining custom struct including fields from both tables
func (r *Repository) GetUsersWithOrders(ctx context.Context) ([]UserWithOrders, error) {
    query := `
        select u.id, u.name, o.id as order_id, o.total
        from users u
        left join orders o on u.id = o.user_id
    `
    // ... implementation
}
```

### Avoid: N+1 query pattern
- For example don't fetch users then loop to fetch orders for each


### Resource Cleanup
- Always close database connections properly.
- Use defer for cleanup when appropriate.
- Handle connection pool exhaustion gracefully.

### Query Logging
- Log slow queries for performance monitoring.
- Include query parameters in logs (sanitized).
- Track query execution times.

## Testing

### Test Database Setup
- Use TestContainers for integration tests.
- Create isolated test databases.
- Reset database state between tests.

### Repository Testing
```go
func TestRepository_GetUser(t *testing.T) {
    // Setup TestContainer with PostgreSQL
    ctx := context.Background()
    container, db := setupTestDB(t, ctx)
    defer container.Terminate(ctx)

    repo := NewPostgresUserRepository(db)

    // Test implementation
}
```

### Transaction Testing, prefer integration testing in this scenario
- Test transaction rollback scenarios.
- Verify isolation levels.
- Test concurrent access patterns.
