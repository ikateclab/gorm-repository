# Test Containers for GORM Repository

This package provides utilities for setting up test containers for database and Redis testing in the GORM Repository project.

## Features

- **Database Testing**: Supports both SQLite in-memory (fast) and PostgreSQL test containers (realistic)
- **Redis Testing**: Provides Redis test container setup
- **Automatic Cleanup**: All containers are automatically cleaned up after tests
- **Easy Integration**: Simple helper functions that integrate with Go's testing framework

## Prerequisites

- Docker must be installed and running on your system
- Go 1.24+ (as specified in go.mod)

## Usage

### Database Testing

The `SetupTestDatabase` function automatically chooses between SQLite and PostgreSQL based on the test mode:

```go
func TestMyRepository(t *testing.T) {
    // Setup database with auto-migration
    dbSetup := SetupTestDatabase(t, &MyModel{})
    defer dbSetup.Cleanup()
    
    db := dbSetup.DB
    
    // Use db for your tests...
}
```

**Test Modes:**
- `go test -short`: Uses SQLite in-memory database (faster, no Docker required)
- `go test`: Uses PostgreSQL test container (more realistic, requires Docker)

### Redis Testing

```go
func TestMyCache(t *testing.T) {
    // Setup Redis test container
    redisSetup := SetupTestRedis(t)
    defer redisSetup.Cleanup()
    
    client := redisSetup.Client
    
    // Use Redis client for your tests...
}
```

### Combined Database and Redis Testing

```go
func TestMyCachedRepository(t *testing.T) {
    // Setup both database and Redis
    dbSetup := SetupTestDatabase(t, &MyModel{})
    defer dbSetup.Cleanup()
    
    redisSetup := SetupTestRedis(t)
    defer redisSetup.Cleanup()
    
    db := dbSetup.DB
    redisClient := redisSetup.Client
    
    // Use both for your tests...
}
```

## Running Tests

### Fast Tests (SQLite in-memory)
```bash
go test -short ./...
```

### Full Tests (with PostgreSQL containers)
```bash
go test ./...
```

### Specific Package Tests
```bash
# Test only the cache package with containers
go test ./cache

# Test only the cache package with SQLite
go test -short ./cache
```

## Container Images Used

- **PostgreSQL**: `postgres:15-alpine`
- **Redis**: `redis:7-alpine`

These images are automatically pulled by testcontainers when needed.

## Benefits

1. **Isolation**: Each test gets its own fresh database and Redis instance
2. **Consistency**: Tests run the same way locally and in CI/CD
3. **Realistic**: PostgreSQL tests use the same database engine as production
4. **Fast Option**: SQLite tests for quick feedback during development
5. **No Manual Setup**: No need to manually start/stop databases for testing

## Troubleshooting

### Docker Issues
- Ensure Docker is running: `docker ps`
- Check Docker permissions if on Linux
- For CI/CD, ensure Docker-in-Docker or Docker socket is available

### Port Conflicts
- Testcontainers automatically assigns random ports, so conflicts are rare
- If issues persist, check for zombie containers: `docker ps -a`

### Performance
- Use `go test -short` for faster feedback during development
- Use full tests before committing or in CI/CD pipelines
- Consider running tests in parallel: `go test -parallel 4`

## Examples

See `example_test.go` for complete working examples of:
- Database-only testing
- Redis-only testing  
- Combined database and Redis testing

## Integration with Existing Tests

To migrate existing tests to use test containers:

1. Replace manual database setup with `SetupTestDatabase()`
2. Replace manual Redis setup with `SetupTestRedis()`
3. Add `defer cleanup()` calls
4. Remove hardcoded connection strings
5. Remove test skipping logic for missing services
