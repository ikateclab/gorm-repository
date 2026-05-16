package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
