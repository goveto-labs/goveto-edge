# Guide for Generated GCO Client

This document is generated automatically by `gco generate`.
## Imports

```go
import (
    "context"
    "database/sql"
    "your-module/gen/client"
    "your-module/gen/query"
    "your-module/gen/model"
)
```

## Client Setup

```go
db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
if err != nil {
    panic(err)
}
defer db.Close()

c := client.New(db, client.WithDialect("postgresql"))
defer c.Close()

ctx := context.Background()
```

## Recommended Usage Pattern

Prefer staged builders for readability and composability:

```go
users, err := c.User.Query().
    Where(query.User.Email.Contains("@example.com")).
    OrderBy(query.User.CreatedAt.Desc()).
    Take(20).
    Do(ctx)
```

## CRUD Builder Pattern

- Create one: `c.<Model>.Create().Set(...).Do(ctx)`
- Update one: `c.<Model>.Update().Where(...).Set(...).Do(ctx)`
- Update many: `c.<Model>.Update().Where(...).Set(...).DoMany(ctx)`
- Delete one: `c.<Model>.Delete().Where(...).Do(ctx)`
- Delete many: `c.<Model>.Delete().Where(...).DoMany(ctx)`

## Bulk Inserts

Use `BulkCreate` for high-volume inserts, idempotent sync jobs, and join table writes:

```go
rows, err := c.Post.BulkCreate(posts).
    OnConflictDoNothing(query.PostIdColumn).
    Returning(query.PostIdColumn).
    BatchSize(1000).
    DoReturningValues(ctx)
```

- `Do(ctx)` executes batches and returns affected rows.
- `DoReturning(ctx)` returns inserted model rows on PostgreSQL.
- `DoReturningValues(ctx)` returns selected `RETURNING` columns as row maps.
- `CreateMany(ctx, data)` remains available and delegates to `BulkCreate(data).Do(ctx)`.

## Raw SQL

Use raw SQL for CTEs, `UPDATE ... FROM`, query-plan-sensitive joins, and PostgreSQL-specific features:

```go
posts, err := client.Raw[model.Post](ctx, c,
    `SELECT id, title, author_id FROM posts WHERE id = $1`, id)
```

- `client.Raw[T](ctx, c, sql, args...)` scans rows into a struct using `db` tags.
- `c.RawRows(ctx, sql, args...)` returns `[]map[string]any` for ad-hoc projections.
- `c.RawExec(ctx, sql, args...)` executes custom SQL and returns affected rows.

## Table and Column Constants

Generated query packages include constants for hand-written SQL:

```go
query.PostTable
query.PostIdColumn
query.PostAuthorIdColumn
```

## Models and Fields

### AuditLog

- Client handle: `c.AuditLog`
- Query namespace: `query.AuditLog`
- Fields:
  - `Id` (String, id)
  - `Actorid` (String, optional)
  - `Actor` (String)
  - `Action` (String)
  - `Resource` (String)
  - `Beforejson` (Json, optional)
  - `Afterjson` (Json, optional)
  - `Createdat` (DateTime)

### ConfigVersion

- Client handle: `c.ConfigVersion`
- Query namespace: `query.ConfigVersion`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Version` (BigInt)
  - `Configjson` (Json)
  - `Hash` (String)
  - `Status` ()
  - `Createdat` (DateTime)

### Node

- Client handle: `c.Node`
- Query namespace: `query.Node`
- Fields:
  - `Id` (String, id)
  - `Region` (String)
  - `Address` (String, unique)
  - `Version` (String)
  - `Heartbeatat` (DateTime, optional)
  - `Status` ()
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### OriginPool

- Client handle: `c.OriginPool`
- Query namespace: `query.OriginPool`
- Fields:
  - `Id` (String, id)
  - `Name` (String)
  - `Protocol` ()
  - `Backends` (Json)
  - `Healthuri` (String)
  - `Timeout` (Int)
  - `Headers` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### Policy

- Client handle: `c.Policy`
- Query namespace: `query.Policy`
- Fields:
  - `Id` (String, id)
  - `Name` (String)
  - `Cachejson` (Json)
  - `Wafjson` (Json)
  - `Ccjson` (Json)
  - `Accessjson` (Json)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### PublishJob

- Client handle: `c.PublishJob`
- Query namespace: `query.PublishJob`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Version` (BigInt)
  - `Targets` (Json)
  - `Status` ()
  - `Resultjson` (Json, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### PurgeJob

- Client handle: `c.PurgeJob`
- Query namespace: `query.PurgeJob`
- Fields:
  - `Id` (String, id)
  - `Siteid` (String)
  - `Type` ()
  - `Value` (String, optional)
  - `Status` ()
  - `Resultjson` (Json, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### Site

- Client handle: `c.Site`
- Query namespace: `query.Site`
- Fields:
  - `Id` (String, id)
  - `Hostname` (String, unique)
  - `Status` ()
  - `Originpoolid` (String)
  - `Policyid` (String)
  - `Version` (BigInt)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### User

- Client handle: `c.User`
- Query namespace: `query.User`
- Fields:
  - `Id` (String, id)
  - `Email` (String, unique)
  - `Passwordhash` (String)
  - `Name` (String)
  - `Role` ()
  - `Status` ()
  - `Lastloginat` (DateTime, optional)
  - `Createdat` (DateTime)
  - `Updatedat` (DateTime)

### UserSession

- Client handle: `c.UserSession`
- Query namespace: `query.UserSession`
- Fields:
  - `Id` (String, id)
  - `Userid` (String)
  - `Tokenhash` (String, unique)
  - `Expiresat` (DateTime)
  - `Createdat` (DateTime)

### When generating application code:

1. Prefer staged builders (`Query/Create/Update/Delete`) over one-shot calls.
2. Use `query.<Model>.<Field>` helpers for all conditions and set operations.
3. For optional fields, use `Set(value)` for non-null values and `SetNull()` to write NULL.
4. Use `DoMany` only when multiple-row side effects are intended.
5. Preserve explicit error handling on every DB operation.
