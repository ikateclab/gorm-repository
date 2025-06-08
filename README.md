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
    ID    uuid.UUID `gorm:"type:text;primary_key"`
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
user := User{ID: uuid.New(), Name: "John", Email: "john@example.com"}
err := userRepo.Create(ctx, user)

// Find by ID
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

```go
type User struct {
    ID    uuid.UUID `gorm:"type:text;primary_key"`
    Name  string
    Email string
    Age   int
}

// Implement Diffable interface
func (u User) Clone() User {
    return u // simple clone for this example
}

func (u User) Diff(other User) map[string]interface{} {
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
    FindMany(ctx context.Context, options ...Option) ([]T, error)
    FindPaginated(ctx context.Context, page int, pageSize int, options ...Option) (*PaginationResult[T], error)
    FindById(ctx context.Context, id uuid.UUID, options ...Option) (T, error)
    FindOne(ctx context.Context, options ...Option) (T, error)
    Create(ctx context.Context, entity T, options ...Option) error
    Save(ctx context.Context, entity T, options ...Option) error
    UpdateById(ctx context.Context, id uuid.UUID, entity T, options ...Option) error
    UpdateByIdWithMask(ctx context.Context, id uuid.UUID, mask map[string]interface{}, entity T, options ...Option) error
    UpdateByIdWithMap(ctx context.Context, id uuid.UUID, values map[string]interface{}, options ...Option) (T, error)
    UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity Diffable[T], updateFunc func(Diffable[T]), options ...Option) error
    DeleteById(ctx context.Context, id uuid.UUID, options ...Option) error
    BeginTransaction() *Tx
    AppendAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
    RemoveAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
    ReplaceAssociation(ctx context.Context, entity T, association string, values interface{}, options ...Option) error
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
