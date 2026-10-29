package plugin

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// pluginName identifies this plugin in GORM's plugin registry.
const pluginName = "gormrepository:cache"

// Plugin is the GORM plugin that installs cache callbacks: read-through
// on queries, tag-based invalidation on writes.
type Plugin struct {
	cache   Cache
	options options

	registry   map[string]*ModelOptions
	registryMu sync.RWMutex

	// txInvalidations holds, per CommitHook, what its transaction's writes
	// invalidate on commit.
	txInvalidations sync.Map // CommitHook -> *txInvalidations
}

// ModelOptions holds per-model configuration, populated via
// RegisterModel[T] or Register.
type ModelOptions struct {
	TTL       time.Duration
	Bypass    bool
	ExtraTags []string
}

// New constructs a new cache plugin.
//
// At minimum a Cache must be supplied; passing nil is allowed for tests
// that exercise key/tag derivation without storage, and causes every
// callback to fall back to the underlying GORM behavior.
func New(cache Cache, opts ...Option) *Plugin {
	o := options{
		defaultTTL:    10 * time.Minute,
		defaultTxMode: TxBypass,
		metrics:       noopMetrics{},
	}
	for _, opt := range opts {
		opt(&o)
	}
	reg := make(map[string]*ModelOptions)
	for table, mo := range o.modelRegistrations {
		reg[table] = mo
	}
	return &Plugin{
		cache:    cache,
		options:  o,
		registry: reg,
	}
}

// Name satisfies gorm.Plugin.
func (p *Plugin) Name() string { return pluginName }

// Initialize satisfies gorm.Plugin. It resolves RegisterModel tables with
// db's NamingStrategy, then registers query, create, update, delete, and
// raw callbacks that implement cache read-through and invalidation.
func (p *Plugin) Initialize(db *gorm.DB) error {
	if err := p.resolveModelTables(db); err != nil {
		return err
	}
	return p.registerCallbacks(db)
}

// resolveModelTables re-keys RegisterModel[T] registrations by the table
// name db's NamingStrategy gives T. RegisterModel only has GORM's default
// naming to go on, which misses custom namers (e.g. a camelCase or
// singular-table strategy) — and a registration keyed by the wrong table
// is silently never applied.
func (p *Plugin) resolveModelTables(db *gorm.DB) error {
	p.registryMu.Lock()
	defer p.registryMu.Unlock()
	for typ, defaultTable := range p.options.modelTypes {
		s, err := schema.Parse(reflect.New(typ).Interface(), &sync.Map{}, db.NamingStrategy)
		if err != nil {
			return fmt.Errorf("cache plugin: resolve table for %s: %w", typ, err)
		}
		if s.Table == defaultTable {
			continue
		}
		if mo, ok := p.registry[defaultTable]; ok {
			delete(p.registry, defaultTable)
			p.registry[s.Table] = mo
		}
	}
	return nil
}

// Cache exposes the underlying storage to callbacks and tests.
func (p *Plugin) Cache() Cache { return p.cache }

// DefaultTTL returns the configured default TTL.
func (p *Plugin) DefaultTTL() time.Duration { return p.options.defaultTTL }

// DefaultTxMode returns the configured default TxMode.
func (p *Plugin) DefaultTxMode() TxMode { return p.options.defaultTxMode }

// SchemaVersion returns the configured schema-version prefix.
func (p *Plugin) SchemaVersion() string { return p.options.schemaVersion }

// ScopeColumns returns the configured scope-column list (read-only copy).
func (p *Plugin) ScopeColumns() []string {
	out := make([]string, len(p.options.scopeColumns))
	copy(out, p.options.scopeColumns)
	return out
}

// Metrics returns the configured Metrics collector. Never nil.
func (p *Plugin) Metrics() Metrics { return p.options.metrics }

// Debug reports whether verbose logging is enabled.
func (p *Plugin) Debug() bool { return p.options.debug }

// Register stores per-model overrides keyed by table name. RegisterModel[T]
// is the typed alternative at construction time.
func (p *Plugin) Register(table string, m ModelOptions) {
	p.registryMu.Lock()
	defer p.registryMu.Unlock()
	p.registry[table] = &m
}

// LookupModel returns the options registered for table, if any.
func (p *Plugin) LookupModel(table string) (ModelOptions, bool) {
	p.registryMu.RLock()
	defer p.registryMu.RUnlock()
	m, ok := p.registry[table]
	if !ok {
		return ModelOptions{}, false
	}
	return *m, true
}
