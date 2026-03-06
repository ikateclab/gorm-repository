# AGENTS.md

This file provides guidance for AI coding agents working in this repository.

## Project Overview

`gorm-repository` (`github.com/ikateclab/gorm-repository`) is a Go library that implements a generic, type-safe repository pattern on top of [GORM](https://gorm.io/). It targets Go 1.24+ and is distributed as a reusable package (no `main` entrypoint).

## Repository Structure

```
/
├── repository.go               # Public interfaces and types (Repository, Diffable, PaginationResult, Option, Tx)
├── gorm_repository.go          # GormRepository[T] implementation; all CRUD, transactions, JSONB diff logic
├── gorm_repository_test.go     # Integration tests using Testcontainers + PostgreSQL
├── integration_test.go         # Integration tests using SQLite in-memory
├── benchmark_test.go           # Benchmark tests using SQLite in-memory
├── go.mod / go.sum             # Module definition and dependency lock
├── utils/
│   ├── camel_case_naming_strategy.go  # GORM Namer that preserves camelCase column names
│   ├── entity_to_map.go               # Reflection-based struct→map for field-mask updates
│   └── tests/
│       ├── test_models.go      # Test entities (TestUser, TestProfile, TestPost, etc.)
│       ├── test_helpers.go     # Builder helpers and DB setup/teardown utilities
│       ├── clone.go            # Auto-generated Clone() methods — do not edit by hand
│       └── diff.go             # Auto-generated Diff() methods — do not edit by hand
└── docs/
    └── JSONB_NESTED_GORM_REPOSITORY_CHANGES.md  # Design notes on JSONB nested-field updates
```

## Commands

### Running Tests

```bash
# Full test suite (requires Docker for Testcontainers / PostgreSQL)
go test ./...

# Run only the SQLite-backed integration or benchmark tests (no Docker needed)
go test ./... -run TestIntegration
go test -bench=. ./...

# Tidy dependencies after adding/removing imports
go mod tidy
```

### Code Generation

`Diff()` and `Clone()` methods for test model types are **auto-generated** — never edit `utils/tests/clone.go` or `utils/tests/diff.go` by hand. Regenerate them after changing `utils/tests/test_models.go`:

```bash
go generate ./utils/tests/...
# Internally runs: go run github.com/ikateclab/gorm-tracked-updates/cmd/gorm-gen@v0.0.6 -package=.
```

## Coding Conventions

### Naming
- Package: `gormrepository` (no hyphens; root package).
- Struct fields: PascalCase (`Id`, `CreatedAt`, `WhatsAppData`).
- Primary key field name: `Id` (not `ID`).
- GORM column names: camelCase via `CamelCaseNamingStrategy` (e.g., `archivedAt`, `whatsAppData`).

### Generics
All repository types are parameterised over `T any`. Keep the type parameter in scope — do not introduce `interface{}` as a workaround.

### Functional Options Pattern
Every repository method accepts variadic `...Option` where `Option` is defined as:

```go
type Option func(*gorm.DB) *gorm.DB
```

Built-in options: `WithTx`, `WithQuery`, `WithQueryStruct`, `WithRelations`. Add new options following the same signature.

### Entities and the `Diffable` Interface
To enable diff-based partial updates, an entity must implement:

```go
type Diffable[T any] interface {
    Clone() *T
    Diff(*T) map[string]interface{}
}
```

`Diff()` must return a **flattened dot-notation map** for nested JSONB fields (e.g., `"whatsAppData.status.mode"`). This format is consumed directly by `processJSONBDiff` in `gorm_repository.go`.

### Associations
Always use `db.Omit(clause.Associations)` when creating or updating records to prevent GORM from auto-saving associations unexpectedly.

### Error Handling
Return errors normally; never panic in the public API.

### Caching
Use `sync.Map` or `sync.RWMutex`-protected maps for caching reflection results or DB column-type lookups (see `entity_to_map.go` and `gorm_repository.go` for existing patterns).

### Primary Keys
All entities use `uuid.UUID` (from `github.com/google/uuid`) with the GORM tag `gorm:"type:text;primary_key"`.

## Testing Guidelines

| Test file | Database | Notes |
|---|---|---|
| `gorm_repository_test.go` | PostgreSQL via Testcontainers | Requires Docker; tests JSONB and full PG feature set |
| `integration_test.go` | SQLite in-memory | Fast; no external dependencies |
| `benchmark_test.go` | SQLite in-memory | Use `-bench=.` flag |

- Use the builder helpers in `utils/tests/test_helpers.go` (`TestUserBuilder`, `SetupTestDBWithConfig`, `CleanupTestDB`) when writing new tests.
- Prefer adding SQLite integration tests for new generic behaviour and PostgreSQL tests only when the feature is PostgreSQL-specific (e.g., JSONB operations).

## Key Implementation Notes

- **JSONB nested updates** use a `jsonb_set` SQL expression built in `buildJSONBSetExpression`. See `docs/JSONB_NESTED_GORM_REPOSITORY_CHANGES.md` for the design rationale.
- **`entity_to_map.go`** converts an entity struct to a `map[string]interface{}` using a field mask; it caches reflection metadata for performance. Nested JSONB fields are serialised as a single JSON value rather than expanded into individual columns.
- **`CamelCaseNamingStrategy`** is opt-in. Consumers who want snake_case columns (GORM default) simply do not register it.

## CI / Release

Tests run as part of the release pipeline (`.github/workflows/release.yml`) triggered by a `v*` tag push. There is no separate PR CI workflow. Always ensure `go test ./...` passes locally before pushing release tags.
