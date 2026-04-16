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
	"time"
)

// Cache is the only thing storage backends must implement.
//
// Implementations must be safe for concurrent use. Get returning
// (nil, false, nil) indicates a miss with no error.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool, error)
	Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration) error
	Invalidate(ctx context.Context, tags []string) error
}

// TxMode controls how the plugin behaves inside an open GORM transaction.
// See the design doc (section 4) for the decision matrix.
type TxMode int

const (
	// TxBypass skips the cache entirely inside any transaction. Default.
	TxBypass TxMode = iota
	// TxDeferred reads from the cache inside a tx; Set and Invalidate are
	// queued and applied on commit.
	TxDeferred
	// TxFull reads and writes the cache inline inside a tx; only
	// invalidations are deferred to commit. Unsafe under rollback — see
	// the design doc before enabling.
	TxFull
)

// Option configures the plugin at construction time.
type Option func(*options)

type options struct {
	defaultTTL    time.Duration
	defaultTxMode TxMode
	schemaVersion string
	debug         bool
	scopeColumns  []string
	metrics       Metrics
}

// WithDefaultTTL sets the default TTL for cached entries. Zero means no
// expiry.
func WithDefaultTTL(ttl time.Duration) Option {
	return func(o *options) { o.defaultTTL = ttl }
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
