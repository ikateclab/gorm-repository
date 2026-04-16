package plugin

import (
	"sync"
	"time"

	"gorm.io/gorm"
)

// pluginName identifies this plugin in GORM's plugin registry.
const pluginName = "gormrepository:cache"

// Plugin is the GORM plugin that installs cache callbacks. The current
// PR-1 wiring registers the plugin and exposes its configuration; the
// query/write callbacks are added in PR-2.
type Plugin struct {
	cache   Cache
	options options

	registry   map[string]*ModelOptions
	registryMu sync.RWMutex
}

// ModelOptions holds per-model configuration. A future Register option
// will populate this map; for now it exists so callbacks can look up
// overrides without API churn.
type ModelOptions struct {
	TTL        time.Duration
	Bypass     bool
	ExtraTags  []string
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
	return &Plugin{
		cache:    cache,
		options:  o,
		registry: make(map[string]*ModelOptions),
	}
}

// Name satisfies gorm.Plugin.
func (p *Plugin) Name() string { return pluginName }

// Initialize satisfies gorm.Plugin. PR-1 only validates the
// configuration; subsequent PRs will register query/create/update/delete
// callbacks here.
func (p *Plugin) Initialize(db *gorm.DB) error {
	// Intentionally a no-op for the foundation PR. Callback registration
	// arrives in PR-2 (Phases 5-6) per the design doc.
	_ = db
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

// Register stores per-model overrides keyed by table name. Currently
// internal-only; a typed Register[T] generic option will land in PR-4.
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
