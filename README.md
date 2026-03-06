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

// Usage with transactions — err must be declared before the defer so
// tx.Finish sees the final error value when the function returns.
var err error
tx := userRepo.BeginTransaction()
defer tx.Finish(&err)

// Find and modify (clone is automatically stored in the transaction)
var user *User
user, err = userRepo.FindById(ctx, userID, gr.WithTx(tx))
if err != nil {
    return err
}
user.Name = "Updated Name"

// Only changed fields will be updated
err = userRepo.UpdateById(ctx, userID, user, gr.WithTx(tx))
```

### In-Place Updates

`UpdateByIdInPlace` and `UpdateInPlace` clone the entity before the mutation, apply the update function, diff the result, and persist only changed fields — all in one call. The difference is that `UpdateByIdInPlace` takes an explicit UUID while `UpdateInPlace` derives the primary key from the entity itself.

```go
// UpdateByIdInPlace: pass the id separately
err := userRepo.UpdateByIdInPlace(ctx, userID, user, func() {
    user.Name = "New Name"
    user.Email = "new@example.com"
})

// UpdateInPlace: id is read from user.Id
err := userRepo.UpdateInPlace(ctx, user, func() {
    user.Name = "New Name"
})
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

// Method 3: Nested transactions
outerTx := userRepo.BeginTransaction()
defer outerTx.Finish(&err)

innerTx := outerTx.BeginTransaction()
defer innerTx.Finish(&err)

err = userRepo.Create(ctx, user1, gr.WithTx(innerTx))
if err != nil {
    return err
}

// Check the underlying GORM transaction for errors
if txErr := outerTx.Error(); txErr != nil {
    return txErr
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

// Max value of a column
maxAge, err := userRepo.Max(ctx, "age")

// Bulk update — map keys are struct field names, values are the new values to set
err = userRepo.BulkUpdate(ctx,
    gr.WithQuery(func(db *gorm.DB) *gorm.DB {
        return db.Where("active = ?", false)
    }),
    map[string]interface{}{"Name": "Inactive User"},
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

### JSON Merge Expression (PostgreSQL)

`BuildJSONMergeExpr` constructs a PostgreSQL `||` merge expression that shallow-merges a JSON value into an existing `json`/`jsonb` column. It automatically detects the column type from `information_schema` and handles `NULL` columns via `COALESCE`.

```go
import (
    gr "github.com/ikateclab/gorm-repository"
)

mergeExpr := gr.BuildJSONMergeExpr(db, "users", "settings", `{"theme":"dark","locale":"en"}`)
err := db.Model(&user).Update("settings", mergeExpr).Error
```

## Requirements

- Go 1.24+
- GORM v1.30+
- UUID support via `github.com/google/uuid`

## Testing

The package includes comprehensive tests with integration tests and benchmarks:

```bash
go test ./...
go test -bench=. ./...
```

## License

This project is licensed under the MIT License.
