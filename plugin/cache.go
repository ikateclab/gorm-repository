// Package plugin implements a GORM plugin that caches query results with
// tag-based invalidation and transaction-aware semantics. See the design
// doc for rationale.
//
// This package is deliberately free of imports from the surrounding
// gormrepository module so it can be extracted to a standalone module in
// the future without refactoring.
package plugin

import (
	"context"
	"reflect"
	"time"
)

// Versions is a backend's snapshot of how far each of a read's tags had
// been invalidated when the read started (tag -> version; a missing tag
// counts as 0). Get returns it and Set receives it back unchanged.
type Versions = map[string]int64

// Cache is the only thing storage backends must implement.
//
// Implementations must be safe for concurrent use. Get receives the tags
// the entry is known to depend on before the query runs, and returns their
// Versions at that moment. Set stores under those versions, and a backend
// never serves a value one of whose tags was invalidated after them —
// otherwise a read overlapping a write could cache the pre-write value
// after the write's invalidation, and serve it until TTL. Versions are
// tracked per tag, not by one global counter, so no single key is touched
// by every read and invalidation.
type Cache interface {
	Get(ctx context.Context, key string, tags []string) (value []byte, hit bool, versions Versions, err error)
	Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration, versions Versions) error
	Invalidate(ctx context.Context, tags []string) error
}

// TxMode controls how the plugin behaves inside an open GORM transaction.
//
// In every mode, a write inside a transaction that exposes a CommitHook
// (the repository's Tx does) invalidates on commit, never before it. A
// plain GORM transaction (db.Begin, db.Transaction) has no hook: reads
// inside it always skip the cache, and its writes invalidate inline.
type TxMode int

const (
	// TxBypass skips the cache for reads inside any transaction. Default.
	TxBypass TxMode = iota
	// TxDeferred serves reads inside a tx from the cache until the tx
	// writes (see PendingTracker). Results read inside the tx are never
	// stored, since another transaction may commit a newer value first.
	TxDeferred
	// TxFull is TxDeferred, but also stores results read inside the tx
	// inline. Unsafe: a snapshot read early in a long transaction can be
	// cached after another transaction already invalidated it.
	TxFull
)

// Option configures the plugin at construction time.
type Option func(*options)

type options struct {
	defaultTTL     time.Duration
	defaultTTLFunc func() time.Duration
	defaultTxMode  TxMode
	schemaVersion  string
	keyPrefix      string
	debug          bool
	scopeColumns   []string
	metrics        Metrics

	// modelRegistrations holds per-model overrides specified via
	// RegisterModel[T]() at construction time. They are transferred
	// to the Plugin's registry in New().
	modelRegistrations map[string]*ModelOptions

	// modelTypes maps each RegisterModel[T] type to the table name it was
	// registered under with GORM's default naming; Initialize re-resolves
	// it with the DB's own NamingStrategy.
	modelTypes map[reflect.Type]string

	// tagStrategy, when set via WithTagStrategy, replaces the plugin's
	// built-in tag derivation entirely. See tag_strategy.go.
	tagStrategy TagStrategy

	recorder EventRecorder
}

// WithDefaultTTL sets the default TTL for cached entries. Zero means no
// expiry. A backend may clamp it: rediscache caps entries at 15 minutes
// by default (zero included), which rediscache.WithMaxTTL changes.
func WithDefaultTTL(ttl time.Duration) Option {
	return func(o *options) { o.defaultTTL = ttl }
}

// WithDefaultTTLFunc sets a function called to compute the TTL for each
// cached entry, instead of one fixed WithDefaultTTL duration. Useful for
// jitter — e.g. a random duration within a range — so entries written
// around the same time don't all expire together and stampede the
// database. Takes precedence over WithDefaultTTL when both are set.
func WithDefaultTTLFunc(fn func() time.Duration) Option {
	return func(o *options) { o.defaultTTLFunc = fn }
}

// WithDefaultTxMode sets the default transaction mode. Callers can
// override per-session via WithTxMode.
func WithDefaultTxMode(mode TxMode) Option {
	return func(o *options) { o.defaultTxMode = mode }
}

// WithSchemaVersion sets a schema-version prefix for cache keys, letting
// callers invalidate the entire cache on a migration by bumping the
// version.
func WithSchemaVersion(v string) Option {
	return func(o *options) { o.schemaVersion = v }
}

// WithKeyPrefix namespaces this plugin's cache keys, e.g. per service or
// database, so two deployments sharing one cache backend can't collide on
// same-named tables. Tags are not prefixed: invalidation stays shared.
func WithKeyPrefix(prefix string) Option {
	return func(o *options) { o.keyPrefix = prefix }
}

// WithDebug enables verbose logging of cache decisions. Intended for
// local development.
func WithDebug(b bool) Option {
	return func(o *options) { o.debug = b }
}

// WithScopeColumns sets the columns whose WHERE-clause values should be
// promoted to cache tags automatically (e.g. "account_id", "tenant_id").
func WithScopeColumns(cols ...string) Option {
	return func(o *options) {
		o.scopeColumns = append([]string(nil), cols...)
	}
}

// WithMetrics injects a Metrics collector. The default is a no-op.
func WithMetrics(m Metrics) Option {
	return func(o *options) { o.metrics = m }
}

// Metrics is a minimal counter interface; implementations live in
// subpackages (e.g. plugin/metrics) so this package stays dependency-light.
type Metrics interface {
	Hit(model string)
	Miss(model string)
	Invalidation(model, reason string)
	PostCommitError()
}

type noopMetrics struct{}

func (noopMetrics) Hit(string)                  {}
func (noopMetrics) Miss(string)                 {}
func (noopMetrics) Invalidation(string, string) {}
func (noopMetrics) PostCommitError()            {}
