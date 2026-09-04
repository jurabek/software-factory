---
name: go-sqlx
description: "Guide to jmoiron/sqlx library covering StructScan, NamedExecContext, DbExecutor interface, connection pooling, and observability integration. Triggers on: go sqlx, go database query, go struct scan, go db executor."
user-invocable: true
---

# Guide to jmoiron/sqlx library

## Using sqlx features.
- Always use `sqlx`'s **Named**, **Queryx**, operations instead of parameterized queries for complex struct mappings.

## Context propagation.
- Always use `QueryxContext`, `SelectContext`, `GetContext`, `NamedExecContext` methods instead of their non-context counterparts.

## Prepared statements
- Use `PrepareNamedContext` for prepared statements with named parameters.

## SQL IN clause
- Use `sqlx.In` for queries with `IN` clause and slice parameters.

## Struct Scanning in sqlx
- Always use **StructScan** to map query results into structs.
    Example:
    ```go
        type Place struct {
            Country       string
            City          sql.NullString
            TelephoneCode int `db:"telcode"`
            Metadata      JSONB `db:"metadata"`
        }

        rows, err := db.QueryxContext(ctx, "select * from place") // or sqlx.NamedQueryContext(ctx, dbExec, query, args) for named queries
        for rows.Next() {
            var p Place
            err = rows.StructScan(&p)
        }
    ```
- When struct contains JSON/JSONB columns use **QueryxContext/QueryRowxContext** over `GetContext/SelectContext` to be able use `sql.Scanner` and `driver.Value` functionalities, same can be applied when you selecting multiple rows.
    Example:
    ```go
        type JSONB map[string]interface{}

        func (j *JSONB) Scan(value any) error {
            switch v := val.(type) {
            case []byte:
                json.Unmarshal(v, &j)
                return nil
            case string:
                json.Unmarshal([]byte(v), &j)
                return nil
            default:
                return fmt.Errorf("unsupported type: %T", v)
            }
        }

        func (j *JSONB) Value() (driver.Value, error) {
            return json.Marshal(j)
        }
    ```

- If querying only single row, use `GetContext` or `QueryRowContext` for better error handling and simplicity
    Example:
    ```go
        var p Place
        err := db.GetContext(ctx, &p, "select * from place where country=$1", country)

        // or
        var p Place
        err := db.QueryRowx("SELECT city, telcode FROM place LIMIT 1").StructScan(&p)

    ```

## Executing DML queries using sqlx

- When executing Update, Insert or Delete statements, use `NamedExecContext` for better readability and maintainability, if there only single parameter required just use $1, no need to use named parameters.
    Example:
    ```go
    type User struct {
        ID    string `db:"id"`
        Name  string `db:"name"`
        Email string `db:"email"`
    }

    // named update or insert
    func UpdateUser(ctx context.Context, dbEx db.DbExecutor, user User) error {
        updateUserQuery := `update users set name = :name, email = :email where id = :id`
        _, err := dbEx.NamedExecContext(ctx, updateUserQuery, user)
        if err != nil {
            return fmt.Errorf("failed to update user: %w", err)
        }
        return nil
    }
    ```

## Using `sqlx.DB`

- Connection Pool
    Example
    ```go
    func NewDB(connString string) (*sqlx.DB, error) {
        db, err := sqlx.Connect("postgres", connString)
        if err != nil {
            return nil, fmt.Errorf("connect to database: %w", err)
        }

        // Configure connection pool
        db.SetMaxOpenConns(25)
        db.SetMaxIdleConns(5)
        db.SetConnMaxLifetime(5 * time.Minute)
        db.SetConnMaxIdleTime(5 * time.Minute)

        return db, nil
    }
    ```

- Define `DbExecutor` global interface in db layer for all DB operation which is compatible with `*sqlx.DB` and `*sqlx.Tx`, and use this interface on each repository methods.
    Example:
    ```go
    // database/db.go
    var (
        _ DbExecutor = (*sqlx.DB)(nil)
        _ DbExecutor = (*sqlx.Tx)(nil)
    )

    type DbExecutor interface {
        sqlx.ExtContext
        GetContext(ctx context.Context, dest any, query string, args ...any) error
        SelectContext(ctx context.Context, dest any, query string, args ...any) error
        NamedExecContext(ctx context.Context, query string, arg any) (sql.Result, error)
        PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error)
        QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
        Rebind(query string) string
        Select(dest any, query string, args ...any) error
    }

    // repositories/repo.go
    type Repository struct {
        db db.DbExecutor
    }

    func (r *Repository) GetUser(ctx context.Context, dbExec db.DbExecutor, id string) (*User, error) {
        var user User
        query := `select id, name, email from users where id = $1`

        err := dbExec.GetContext(ctx, &user, query, id)
        if err != nil {
            return nil, fmt.Errorf("get user: %w", err)
        }
        return &user, nil
    }

    // main.go
    dbx := NewDB(connStr)
    repo := NewRepository(dbx)

    ```


## Observability
- Use `github.com/XSAM/otelsql` and wrap the `sql.DB`
    Example
    ```go
        db := otelsql.OpenDB(stdlib.GetConnector(*pgxCfg),
            otelsql.WithAttributes(semconv.DBNamespace(componentName)),
            otelsql.WithSpanOptions(otelsql.SpanOptions{
                OmitConnResetSession: true,
                OmitRows:             true,
            }),
        )
        ddx := sqlx.NewDb(db, "pgx")

        err = otelsql.RegisterDBStatsMetrics(db, otelsql.WithAttributes(
            semconv.DBSystemPostgreSQL,
            semconv.DBNamespace(componentName),
        ))
    ```
