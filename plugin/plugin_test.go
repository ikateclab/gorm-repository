package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin/memory"
)

func TestNew_Defaults(t *testing.T) {
	p := New(nil)
	require.NotNil(t, p)
	assert.Equal(t, pluginName, p.Name())
	assert.Equal(t, 10*time.Minute, p.DefaultTTL())
	assert.Equal(t, TxBypass, p.DefaultTxMode())
	assert.Empty(t, p.SchemaVersion())
	assert.Empty(t, p.ScopeColumns())
	assert.False(t, p.Debug())
	assert.NotNil(t, p.Metrics(), "Metrics() must never return nil")
}

func TestNew_Options(t *testing.T) {
	p := New(nil,
		WithDefaultTTL(time.Second),
		WithDefaultTxMode(TxDeferred),
		WithSchemaVersion("v1"),
		WithScopeColumns("account_id", "tenant_id"),
		WithDebug(true),
	)
	assert.Equal(t, time.Second, p.DefaultTTL())
	assert.Equal(t, TxDeferred, p.DefaultTxMode())
	assert.Equal(t, "v1", p.SchemaVersion())
	assert.Equal(t, []string{"account_id", "tenant_id"}, p.ScopeColumns())
	assert.True(t, p.Debug())
}

func TestPlugin_Initialize(t *testing.T) {
	db := newTestDB(t)
	p := New(nil)
	require.NoError(t, p.Initialize(db))
}

func TestPlugin_Register(t *testing.T) {
	p := New(nil)
	p.Register("users", ModelOptions{TTL: 5 * time.Second, ExtraTags: []string{"hot"}})
	got, ok := p.LookupModel("users")
	require.True(t, ok)
	assert.Equal(t, 5*time.Second, got.TTL)
	assert.Equal(t, []string{"hot"}, got.ExtraTags)

	_, ok = p.LookupModel("missing")
	assert.False(t, ok)
}

func TestNoopMetrics(t *testing.T) {
	// Doesn't really assert anything, but exercises every method to
	// keep coverage honest and ensure the no-op is safe to call.
	var m Metrics = noopMetrics{}
	m.Hit("user")
	m.Miss("user")
	m.Invalidation("user", "create")
	m.PostCommitError()
}

type unparseableRel struct {
	ID    uint
	Other struct{ X int } `gorm:"foreignKey:Nope"`
}

// Initialize fails, rather than silently never applying a registration,
// when a RegisterModel type can't be parsed, or when one of its callbacks
// can't be registered.
func TestPlugin_Initialize_Errors(t *testing.T) {
	t.Run("unsupported model type", func(t *testing.T) {
		err := newTestDB(t).Use(New(memory.New(), RegisterModel[int]()))
		assert.ErrorContains(t, err, "resolve table for int")
	})
	t.Run("invalid relation", func(t *testing.T) {
		err := newTestDB(t).Use(New(memory.New(), RegisterModel[unparseableRel]()))
		assert.ErrorContains(t, err, "resolve table for plugin.unparseableRel")
	})

	// A callback whose before/after constraints contradict each other
	// breaks every later registration on its chain.
	chains := map[string]func(*gorm.DB) error{
		"query": func(db *gorm.DB) error {
			return db.Callback().Query().Before("gorm:query").After("gorm:preload").Register("test:conflict", func(*gorm.DB) {})
		},
		"create": func(db *gorm.DB) error {
			return db.Callback().Create().Before("gorm:begin_transaction").After("gorm:create").Register("test:conflict", func(*gorm.DB) {})
		},
		"update": func(db *gorm.DB) error {
			return db.Callback().Update().Before("gorm:begin_transaction").After("gorm:update").Register("test:conflict", func(*gorm.DB) {})
		},
		"delete": func(db *gorm.DB) error {
			return db.Callback().Delete().Before("gorm:begin_transaction").After("gorm:delete").Register("test:conflict", func(*gorm.DB) {})
		},
		"raw": func(db *gorm.DB) error {
			if err := db.Callback().Raw().Register("test:first", func(*gorm.DB) {}); err != nil {
				return err
			}
			return db.Callback().Raw().Before("gorm:raw").After("test:first").Register("test:conflict", func(*gorm.DB) {})
		},
		"row": func(db *gorm.DB) error {
			if err := db.Callback().Row().Register("test:first", func(*gorm.DB) {}); err != nil {
				return err
			}
			return db.Callback().Row().Before("gorm:row").After("test:first").Register("test:conflict", func(*gorm.DB) {})
		},
	}
	for name, breakChain := range chains {
		t.Run("callback conflict on "+name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
			require.NoError(t, err)
			require.Error(t, breakChain(db), "the chain must be broken before the plugin installs")
			assert.ErrorContains(t, db.Use(New(memory.New())), "conflicting callback")
		})
	}
}
