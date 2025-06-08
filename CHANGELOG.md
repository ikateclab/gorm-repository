# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
- `UpdateById()` - Update entity by ID with diff support
- `UpdateByIdWithMask()` - Update with field mask
- `UpdateByIdWithMap()` - Update with map values
- `UpdateByIdInPlace()` - Update with callback function
- `DeleteById()` - Delete entity by ID
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
