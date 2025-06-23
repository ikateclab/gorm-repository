# GORM Repository Cache

A Redis-based caching layer for the GORM Repository with tag-based invalidation and multi-tenant support.

## Features

- **Tag-based Cache Invalidation**: Efficiently invalidate related cache entries
- **Multi-tenant Support**: Account-based cache isolation
- **Cache-aside Pattern**: Automatic cache population and invalidation
- **Query-aware Caching**: Cache keys include full query parameters
- **Debug Logging**: Optional detailed logging for cache operations
- **Transaction Safety**: Proper cache invalidation on write operations

## Architecture

The caching system consists of three main components:

1. **CachedGormRepository[T]** - Wraps your repository with caching capabilities
2. **ResourceCache** - High-level cache manager implementing cache-aside pattern
3. **TagCache** - Low-level Redis operations with tag-based invalidation

## Quick Start

### 1. Setup Dependencies

```go
import (
    "github.com/go-redis/redis/v8"
    "github.com/ikateclab/gorm-repository/cache"
    gormrepository "github.com/ikateclab/gorm-repository"
)
```

### 2. Create Cache Components

```go
// Setup Redis client
redisClient := redis.NewClient(&redis.Options{
    Addr:     "localhost:6379",
    Password: "",
    DB:       0,
})

// Create cache components
logger := cache.NewSimpleLogger()
tagCache := cache.NewTagCache(redisClient)
resourceCache := cache.NewResourceCache(logger, tagCache, "v1.0.0", true) // debug enabled

// Create cached repository
userRepo := cache.NewCachedGormRepository[*User](db, resourceCache, "v1.0.0", true)
```

### 3. Define Your Entity

Your entity must implement the `Diffable` interface for cache invalidation:

```go
type User struct {
    ID        uuid.UUID `gorm:"type:text;primary_key" json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    AccountId string    `json:"accountId"` // For multi-tenant caching
}

// Implement Diffable interface
func (u *User) Clone() *User {
    if u == nil {
        return nil
    }
    clone := *u
    return &clone
}

func (u *User) Diff(other *User) map[string]interface{} {
    diff := make(map[string]interface{})
    
    if other == nil {
        diff["name"] = u.Name
        diff["email"] = u.Email
        diff["accountId"] = u.AccountId
        return diff
    }
    
    if u.Name != other.Name {
        diff["name"] = u.Name
    }
    if u.Email != other.Email {
        diff["email"] = u.Email
    }
    if u.AccountId != other.AccountId {
        diff["accountId"] = u.AccountId
    }
    
    return diff
}
```

### 4. Use the Cached Repository

```go
ctx := context.Background()

// Create (invalidates cache)
user := &User{ID: uuid.New(), Name: "John", Email: "john@example.com"}
err := userRepo.Create(ctx, user)

// Read operations (cached)
foundUser, err := userRepo.FindById(ctx, user.ID)           // Cache miss, stores in cache
foundUser2, err := userRepo.FindById(ctx, user.ID)          // Cache hit

users, err := userRepo.FindMany(ctx, 
    gormrepository.WithQuery(func(db *gorm.DB) *gorm.DB {
        return db.Where("account_id = ?", "account-123")
    }),
)

// Pagination (cached)
result, err := userRepo.FindPaginated(ctx, 1, 10)

// Update (invalidates cache)
foundUser.Name = "John Smith"
err = userRepo.UpdateById(ctx, foundUser.ID, foundUser)

// Delete (invalidates cache)
err = userRepo.DeleteById(ctx, user.ID)
```

## Cache Key Strategy

Cache keys are generated using:
- Database schema version
- Resource name (entity type)
- MD5 hash of query parameters

Format: `{dbVersion}:{resourceName}:{queryHash}`

## Tag-based Invalidation

The system uses Redis sets to track relationships between cache entries:

- **Entity tags**: `{ResourceName}:{entityId}` - for specific entities
- **Account tags**: `{ResourceName}:{accountId}:list` - for account-based lists
- **General tags**: `{ResourceName}:no-account:list` - for general lists

When an entity is modified, all related cache entries are invalidated.

## Configuration

### Constructor Parameters

```go
// CachedGormRepository
NewCachedGormRepository[T](
    db *gorm.DB,              // GORM database instance
    resourceCache *ResourceCache,  // Cache manager
    dbSchemaVersion string,    // Schema version for cache invalidation
    debugEnabled bool,         // Enable debug logging
)

// ResourceCache
NewResourceCache(
    logger Logger,            // Logger implementation (can be nil)
    tagCache *TagCache,       // Redis tag cache
    dbSchemaVersion string,   // Schema version
    debugEnabled bool,        // Enable debug logging
)
```

### Debug Logging

When debug logging is enabled, you'll see:
- Cache hits and misses with hit ratios
- Cache key generation
- Tag invalidation operations

## Supported Operations

All Repository interface methods are supported:

- **Read Operations** (cached): `FindMany`, `FindById`, `FindOne`, `FindPaginated`
- **Write Operations** (invalidate cache): `Create`, `Save`, `UpdateById`, `UpdateByIdWithMask`, `UpdateByIdWithMap`, `UpdateByIdInPlace`, `UpdateInPlace`, `DeleteById`
- **Association Operations** (invalidate cache): `AppendAssociation`, `RemoveAssociation`, `ReplaceAssociation`

## Testing

Run the tests with Redis available:

```bash
# Start Redis
docker run -d -p 6379:6379 redis:alpine

# Run tests
go test ./cache -v
```

## Performance Considerations

- Cache timeout: 1-3 hours with randomization to prevent thundering herd
- Query extraction uses GORM's dry-run mode for minimal overhead
- Tag-based invalidation scales with the number of related entities
- Redis pipelining is used for batch operations

## Multi-tenant Support

The system automatically detects `AccountId` fields in entities and creates account-specific cache tags. This enables:

- Isolated caching per tenant
- Efficient invalidation of tenant-specific data
- Proper cache isolation in multi-tenant applications

## Error Handling

- Cache failures don't break application functionality
- Fallback to database when cache is unavailable
- Debug logging for troubleshooting cache issues
