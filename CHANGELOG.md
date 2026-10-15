# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Query caching plugin** (`plugin`), with in-memory (`plugin/memory`) and Redis (`plugin/rediscache`) backends:
  - tag-based invalidation on creates, updates, deletes, `db.Exec` and raw `UPDATE … RETURNING`;
  - read-overlap protection through per-tag invalidation versions (`cache:ver:{tag}` in Redis);
  - transaction modes (`TxBypass`, `TxDeferred`, `TxFull`), with invalidation deferred to commit for the repository's `Tx`;
  - per-model settings (`RegisterModel`), key namespaces (`WithKeyPrefix`), schema versions, metrics and event recording;
  - a configurable Redis entry cap (`rediscache.WithMaxTTL`, 15 minutes by default).
- **Nested transactions**: `Tx.BeginTransaction` creates a savepoint. Committing it hands its hooks to the parent; rolling it back undoes only its own writes.
- Pull-request CI (`.github/workflows/test.yml`): `go mod tidy` check, `gofmt`, `go vet` and `go test -race`.
- Cache and codec benchmarks.

### Changed
- `Tx.Commit` and `Tx.Rollback` are safe to call concurrently and finish the transaction, running its hooks, exactly once.
- A failed outermost `Tx.Commit` marks the transaction rolled back and runs its rollback hooks; it still returns the error.
- The query cache does less work per query: a hit from memory takes about a third less time, building a cache key allocates once instead of 16 times (the keys themselves are unchanged), and a batch write of a model without `CacheTags` is no longer walked row by row.
- `rediscache` stores entries in a binary envelope instead of JSON with a base64-encoded value: about a quarter less Redis memory per entry, and a Redis read of a 100-row result about twice as fast (the whole hit, decoding included, about a third faster). Entries in the old envelope read as misses.

### Fixed
- `Tx.BeginTransaction` on a `Tx` used to fail while still sharing the parent's transaction, so committing the child committed the parent.
- `UpdateById` in a nested transaction diffed against a blank entity, silently dropping changes to zero values; it now uses the snapshot an ancestor took.
- `EntityToMap` (and so `UpdateByIdWithMask` and `BulkUpdate`) could write a nested field under another type's JSON name, when two nested types had a field of the same name.

## [1.0.0] - 2024-12-19

### Added
- **Generic Repository Pattern**: Type-safe repository operations using Go generics
- **Core Repository Interface**: Complete CRUD operations with functional options
- **Transaction Management**: Built-in transaction support with automatic rollback/commit
- **Entity Diffing**: Track and update only changed fields using the `Diffable` interface
- **Pagination Support**: Built-in pagination with comprehensive metadata
- **Association Management**: Append, remove, and replace entity associations
- **Flexible Querying**: Functional options for customizing queries including:
  - `WithRelations()` for preloading associations
  - `WithQuery()` for custom query functions
  - `WithQueryStruct()` for struct-based queries
  - `WithTx()` for transaction support
- **Utilities Package**: 
  - CamelCase naming strategy for GORM
  - Entity-to-map conversion utilities
- **Comprehensive Test Suite**: Unit tests, integration tests, and benchmarks
- **Complete Documentation**: README with examples and API documentation

### Repository Methods
- `FindMany()` - Find multiple entities with options
- `FindPaginated()` - Find entities with pagination
- `FindById()` - Find entity by UUID
- `FindOne()` - Find single entity
- `Create()` - Create new entity
- `Save()` - Save entity (create or update)
- `UpdateById()` - Update entity by Id with diff support
- `UpdateByIdWithMask()` - Update with field mask
- `UpdateByIdWithMap()` - Update with map values
- `UpdateByIdInPlace()` - Update with callback function
- `DeleteById()` - Delete entity by Id
- `BeginTransaction()` - Start transaction
- `AppendAssociation()` - Add associations
- `RemoveAssociation()` - Remove associations
- `ReplaceAssociation()` - Replace associations

### Requirements
- Go 1.24+
- GORM v1.30+
- UUID support via `github.com/google/uuid`

### Initial Release
This is the initial stable release of the GORM Repository package, providing a complete generic repository pattern implementation for GORM with advanced features for modern Go applications.
