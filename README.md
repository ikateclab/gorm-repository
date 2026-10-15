# WIP ⚠️ 

This package is a Work In Progress and production use is not recommended


# GORM Repository

A generic repository pattern implementation for GORM with advanced features including transaction management, entity diffing, and pagination.

## Features

- **Generic Repository Pattern**: Type-safe repository operations using Go generics
- **Transaction Management**: Built-in transaction support with automatic rollback/commit
- **Entity Diffing**: Track and update only changed fields using the `Diffable` interface
- **Pagination**: Built-in pagination with comprehensive metadata
- **Association Management**: Append, remove, and replace entity associations
- **Flexible Querying**: Functional options for customizing queries
- **Utilities**: CamelCase naming strategy and entity-to-map conversion
- **Query Caching**: Optional GORM plugin with in-memory and Redis backends, tag-based invalidation and transaction-aware semantics

## Installation

```bash
go get github.com/ikateclab/gorm-repository
```

## Quick Start

### Basic Usage

```go
import (
    gr "github.com/ikateclab/gorm-repository"
    "gorm.io/gorm"
)

// Create a repository for your entity
type User struct {
    Id    uuid.UUID `gorm:"type:text;primary_key"`
    Name  string
    Email string
    Age   int
}

// Initialize repository
db := // your GORM database instance
userRepo := gr.NewGormRepository[User](db)

// Basic operations
ctx := context.Background()

// Create
user := User{Id: uuid.New(), Name: "John", Email: "john@example.com"}
err := userRepo.Create(ctx, user)

// Find by Id
user, err := userRepo.FindById(ctx, userID)

// Find many with options
users, err := userRepo.FindMany(ctx,
    gr.WithQuery(func(db *gorm.DB) *gorm.DB {
        return db.Where("age > ?", 18)
    }),
)

// Pagination
result, err := userRepo.FindPaginated(ctx, 1, 10) // page 1, 10 items per page
```

### Entity Diffing

Implement the `Diffable` interface to enable smart updates:
Use this project https://github.com/ikateclab/gorm-tracked-updates to generate the `Diff` and `Clone` methods for models.

```go
type User struct {
    Id    uuid.UUID `gorm:"type:text;primary_key"`
    Name  string
    Email string
    Age   int
}


// Implement the `Clone` method for the `Diffable` interface
func (u *User) Clone() *User {
    // Create a new User with the same attributes
    return &User{
        Id:    u.Id,
        Name:  u.Name,
        Email: u.Email,
        Age:   u.Age,
    }
}

// Implement the `Diff` method for the `Diffable` interface
func (u *User) Diff(other *User) map[string]interface{} {
    diff := make(map[string]interface{})
    if u.Name != other.Name {
        diff["name"] = u.Name
    }
    if u.Email != other.Email {
        diff["email"] = u.Email
    }
    if u.Age != other.Age {
        diff["age"] = u.Age
    }
    return diff
}

// Usage with transactions
tx := userRepo.BeginTransaction()
defer tx.Finish(&err)

// Find and modify
user, err := userRepo.FindById(ctx, userID, gr.WithTx(tx))
user.Name = "Updated Name"

// Only changed fields will be updated
err = userRepo.UpdateById(ctx, userID, user, gr.WithTx(tx))
```

### Transaction Management

```go
// Method 1: Manual transaction management
tx := userRepo.BeginTransaction()
defer func() {
    if err != nil {
        tx.Rollback()
    } else {
        tx.Commit()
    }
}()

err = userRepo.Create(ctx, user1, gr.WithTx(tx))
if err != nil {
    return err
}

err = userRepo.Create(ctx, user2, gr.WithTx(tx))
if err != nil {
    return err
}

// Method 2: Automatic transaction management
tx := userRepo.BeginTransaction()
defer tx.Finish(&err) // Automatically commits or rolls back based on err

err = userRepo.Create(ctx, user1, gr.WithTx(tx))
if err != nil {
    return err
}

err = userRepo.Create(ctx, user2, gr.WithTx(tx))

// Nested transactions use savepoints
child := tx.BeginTransaction()
if err := userRepo.Create(ctx, user3, gr.WithTx(child)); err != nil {
    child.Rollback() // undoes only the child's writes
} else {
    child.Commit() // released into tx: committed (or rolled back) with it
}
```

### Advanced Querying

```go
// With relations
users, err := userRepo.FindMany(ctx,
    gr.WithRelations("Profile", "Posts"),
)

// Custom query
users, err := userRepo.FindMany(ctx,
    gr.WithQuery(func(db *gorm.DB) *gorm.DB {
        return db.Where("age BETWEEN ? AND ?", 18, 65).
                 Order("created_at DESC")
    }),
)

// Query with struct
users, err := userRepo.FindMany(ctx,
    gr.WithQueryStruct(map[string]interface{}{
        "active": true,
        "age":    25,
    }),
)
```

### Association Management

```go
// Append associations
err = userRepo.AppendAssociation(ctx, user, "Posts", []Post{newPost})

// Remove associations
err = userRepo.RemoveAssociation(ctx, user, "Posts", []Post{oldPost})

// Replace associations
err = userRepo.ReplaceAssociation(ctx, user, "Posts", []Post{post1, post2})
```

## Repository Interface

The repository implements the following interface:

```go
type Repository[T any] interface {
    FindMany(ctx context.Context, options ...Option) ([]*T, error)
    FindPaginated(ctx context.Context, page int, pageSize int, options ...Option) (*PaginationResult[*T], error)
    FindById(ctx context.Context, id uuid.UUID, options ...Option) (*T, error)
    FindOne(ctx context.Context, options ...Option) (*T, error)
    Max(ctx context.Context, column string, options ...Option) (int, error)
    Create(ctx context.Context, entity *T, options ...Option) error
    Save(ctx context.Context, entity *T, options ...Option) error
    BulkUpdate(ctx context.Context, where Option, mask map[string]interface{}, options ...Option) error
    UpdateById(ctx context.Context, id uuid.UUID, entity *T, options ...Option) error
    UpdateByIdWithMask(ctx context.Context, id uuid.UUID, mask map[string]interface{}, entity *T, options ...Option) error
    UpdateByIdWithMap(ctx context.Context, id uuid.UUID, values map[string]interface{}, options ...Option) (*T, error)
    UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity *T, updateFunc func(), options ...Option) error
    UpdateInPlace(ctx context.Context, entity *T, updateFunc func(), options ...Option) error
    DeleteById(ctx context.Context, id uuid.UUID, options ...Option) error
    BeginTransaction() *Tx
    AppendAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error
    RemoveAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error
    ReplaceAssociation(ctx context.Context, entity *T, association string, values interface{}, options ...Option) error
    GetDB() *gorm.DB
}
```

## Utilities

### CamelCase Naming Strategy

```go
import "github.com/ikateclab/gorm-repository/utils"

db, err := gorm.Open(sqlite.Open("test.db"), &gorm.Config{
    NamingStrategy: utils.CamelCaseNamingStrategy{},
})
```

### Entity to Map Conversion

```go
import "github.com/ikateclab/gorm-repository/utils"

fields := map[string]interface{}{
    "Name":  nil,
    "Email": nil,
    "Age":   nil,
}

updateMap, err := utils.EntityToMap(fields, user)
// Returns: map[string]interface{}{"name": "John", "email": "john@example.com", "age": 25}
```

## Query Caching

The `plugin` package is an optional GORM plugin that caches query results and invalidates them on writes. It works on any `*gorm.DB`, including the one behind a repository, and calling code doesn't change.

```go
import (
    "github.com/ikateclab/gorm-repository/plugin"
    "github.com/ikateclab/gorm-repository/plugin/memory"
    "github.com/ikateclab/gorm-repository/plugin/rediscache"
)

// In-process: tests, or a single instance.
var cache plugin.Cache = memory.New(memory.WithMaxEntries(10_000))
// Or shared across instances, with any redis.UniversalClient:
cache = rediscache.New(redisClient)

err := db.Use(plugin.New(cache,
    plugin.WithDefaultTTL(10*time.Minute),
    plugin.WithKeyPrefix("billing-api"), // keeps services sharing a cache apart
    plugin.RegisterModel[User](plugin.ModelWithTTL(2*time.Minute)),
    plugin.RegisterModel[AuditLog](plugin.ModelWithBypass()),
))
```

Other options: `WithDefaultTTLFunc` (e.g. jittered TTLs), `WithDefaultTxMode`, `WithSchemaVersion` (bump to drop every entry after a migration), `WithScopeColumns`, `ModelWithExtraTags`, `WithTagStrategy`, `WithMetrics` and `WithEventRecorder`. Per call, `plugin.Bypass(db)` skips the cache and `plugin.WithTxMode(db, mode)` overrides the transaction mode.

### How invalidation works

- Each cached result is tagged with the table it read (`table:users`), plus any tags from `WithScopeColumns`, `ModelWithExtraTags`, entities implementing `CacheTaggable`, or a custom `TagStrategy`.
- Creates, updates and deletes invalidate the tags of what they wrote. So do raw writes: `db.Exec(...)`, and `UPDATE … RETURNING` run through `db.Raw(...)`.
- A read that overlaps a write is never cached: the backend notes each tag's invalidation version when the read starts, and refuses an entry whose tags were invalidated since.
- Not-found results are cached too, and a hit raises `gorm.ErrRecordNotFound` again for `First`/`Take`/`Last`.

### What always goes to the database

- Queries that may read a table their tag doesn't cover: joins, subqueries, `EXISTS`/`UNION`, raw SQL (`db.Raw(...).Find`) and aliased or multi-table `Table(...)` expressions.
- Locking reads (`FOR UPDATE` / `FOR SHARE`) and `DryRun` statements.
- Destinations that are maps or already-populated structs.
- Models registered with `ModelWithBypass()`, and sessions marked with `plugin.Bypass(db)`.

### Transactions

Inside the repository's `Tx`, invalidation always waits for the commit, and a rollback invalidates nothing. Nested transactions hand their invalidations to their parent, so they run when the outermost transaction commits. Reads follow the transaction mode:

| Mode | Reads inside a transaction | Results read inside it are cached |
|---|---|---|
| `TxBypass` (default) | always go to the database | no |
| `TxDeferred` | served from the cache until the transaction writes | no |
| `TxFull` | served from the cache until the transaction writes | yes (unsafe: can cache a stale snapshot) |

Plain GORM transactions (`db.Transaction`, `db.Begin`) give the plugin no commit hook: reads inside them skip the cache, but their writes invalidate before the commit, leaving a short window in which a concurrent reader can re-cache the old row. Use the repository's `Tx` where that matters.

### Redis backend

- Requires Redis 5 or newer, with server clocks roughly in sync (NTP). Single nodes, Cluster and Ring all work, through any `redis.UniversalClient`.
- Entries live at most 15 minutes by default, whatever TTL the plugin asks for (including 0, "no expiry"). Change the cap with `rediscache.New(client, rediscache.WithMaxTTL(d))`. It exists because each tag's invalidation counter (`cache:ver:{tag}`) expires shortly after the longest entry could, and an entry outliving its counters could otherwise be served as valid.
- To share Redis with the Node.js backend, both sides must bump the `cache:ver:{tag}` counters the same way (see `bumpScript` in `plugin/rediscache/rediscache.go`); otherwise writes from one side don't invalidate the other's entries. Each side keeps its own data keys (`cache:go:…` here).
- Entries are stored in a compact binary envelope. Ones written in the JSON envelope earlier versions used read as misses and are rewritten by the next read, so while a rolling deploy runs both versions, a key the other version wrote last costs a miss.

> **Note:** the Node.js side of the `cache:ver:{tag}` format hasn't been confirmed yet.

### Known limitations

- Plain GORM transactions invalidate before their commit (see above).
- An autocommit `UPDATE … RETURNING` read through `db.Raw(...).Rows()` or `.Row()` invalidates just before the database commits it: GORM hands back the raw rows, so there's nothing to hook when they close.

### Overhead

Indicative medians from `go test -run '^$' -bench . -benchmem ./plugin/ ./plugin/rediscache/`, with Postgres 16 and Redis 7 both running locally (4 vCPU, 2.1 GHz Xeon):

| Operation | No cache | With cache |
|---|---|---|
| Find by id, hit | 122 µs | Redis 73 µs · memory 8 µs |
| Find by id, miss | 122 µs | Redis 286 µs |
| Find 100 rows, hit | 494 µs | Redis 184 µs |
| Update (invalidates) | 691 µs | Redis 927 µs |

Both servers being local, their round trips cost about the same, so a hit saves more against a remote or busy database than shown here. A miss pays a cache lookup and a store on top of the query. A hit served from memory costs about 8 µs in all, decoding included.

## Requirements

- Go 1.24+
- GORM v1.30+
- UUID support via `github.com/google/uuid`
- Redis 5+ for the Redis cache backend
- Docker to run the full test suite

## Testing

The package includes comprehensive tests with integration tests and benchmarks. The full suite needs Docker: PostgreSQL and Redis run via Testcontainers.

```bash
go test ./...
go test -bench=. ./...
```

Pull requests run `go vet`, a `gofmt` check and `go test -race ./...` in CI (`.github/workflows/test.yml`).

## License

This project is licensed under the MIT License.
