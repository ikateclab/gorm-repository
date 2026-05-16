package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// testUser is a simple model for callback tests.
type testUser struct {
	ID   uint   `gorm:"primarykey"`
	Name string
}

func (testUser) TableName() string { return "test_users" }

// cachedTestUser implements CacheTaggable for invalidation tests.
type cachedTestUser struct {
	ID   uint   `gorm:"primarykey"`
	Name string
}

func (cachedTestUser) TableName() string { return "cached_test_users" }

func (u *cachedTestUser) CacheTags() []string {
	if u.ID == 0 {
		return nil
	}
	return []string{"user:" + itoa(int(u.ID))}
}

// itoa is a simple int-to-string for test tag generation.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// testMetrics collects cache events for assertions.
type testMetrics struct {
	hits          int
	misses        int
	invalidations int
	lastModel     string
	lastReason    string
}

func (m *testMetrics) Hit(model string)                  { m.hits++; m.lastModel = model }
func (m *testMetrics) Miss(model string)                 { m.misses++; m.lastModel = model }
func (m *testMetrics) Invalidation(model, reason string) { m.invalidations++; m.lastModel = model; m.lastReason = reason }
func (m *testMetrics) PostCommitError()                  {}

func setupCachedDB(t *testing.T, opts ...Option) (*gorm.DB, *memory.Cache, *testMetrics) {
	t.Helper()
	mc := memory.New()
	met := &testMetrics{}

	defaults := []Option{
		WithDefaultTTL(5 * time.Minute),
		WithMetrics(met),
	}
	allOpts := append(defaults, opts...)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	p := New(mc, allOpts...)
	require.NoError(t, db.Use(p))

	require.NoError(t, db.AutoMigrate(&testUser{}, &cachedTestUser{}))
	return db, mc, met
}

func TestCallback_QueryMiss_ThenHit(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	// Seed data.
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// First query: cache miss → hits DB → populates cache.
	var u1 testUser
	require.NoError(t, db.First(&u1, 1).Error)
	assert.Equal(t, "alice", u1.Name)
	assert.Equal(t, 1, met.misses)
	assert.Equal(t, 0, met.hits)
	assert.Equal(t, 1, mc.Len())

	// Second query: cache hit → no DB round-trip.
	var u2 testUser
	require.NoError(t, db.First(&u2, 1).Error)
	assert.Equal(t, "alice", u2.Name)
	assert.Equal(t, 1, met.misses)
	assert.Equal(t, 1, met.hits)
}

func TestCallback_CreateInvalidates(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	// Seed and cache a query.
	require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "alice"}).Error)

	var users []cachedTestUser
	require.NoError(t, db.Find(&users).Error)
	assert.Len(t, users, 1)
	assert.Equal(t, 1, mc.Len()) // query result cached

	// Create a new user → should invalidate the table tag.
	met.invalidations = 0
	require.NoError(t, db.Create(&cachedTestUser{ID: 2, Name: "bob"}).Error)
	assert.True(t, met.invalidations > 0, "create should trigger invalidation")

	// Cache should be empty after invalidation.
	assert.Equal(t, 0, mc.Len(), "table-level invalidation should clear cached queries")
}

func TestCallback_UpdateInvalidates(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "alice"}).Error)

	// Cache a query.
	var u cachedTestUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, 1, mc.Len())

	// Update → invalidates.
	met.invalidations = 0
	require.NoError(t, db.Model(&cachedTestUser{ID: 1}).Update("name", "alice-updated").Error)
	assert.True(t, met.invalidations > 0)
	assert.Equal(t, 0, mc.Len())
}

func TestCallback_DeleteInvalidates(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "alice"}).Error)

	// Cache a query.
	var u cachedTestUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, 1, mc.Len())

	// Delete → invalidates.
	met.invalidations = 0
	require.NoError(t, db.Delete(&cachedTestUser{}, 1).Error)
	assert.True(t, met.invalidations > 0)
	assert.Equal(t, 0, mc.Len())
}

func TestCallback_Bypass_SkipsCache(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// Bypassed query should not populate cache.
	var u testUser
	require.NoError(t, Bypass(db).First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)
	assert.Equal(t, 0, mc.Len(), "bypass should skip cache population")
	assert.Equal(t, 0, met.misses, "bypass should not count as miss")
	assert.Equal(t, 0, met.hits)
}

func TestCallback_ModelBypass(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	p := extractPlugin(t, db)
	p.Register("test_users", ModelOptions{Bypass: true})

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)
	assert.Equal(t, 0, mc.Len(), "model-level bypass should skip cache")
	assert.Equal(t, 0, met.misses)
}

func TestCallback_TxBypass_SkipsCache(t *testing.T) {
	db, _, met := setupCachedDB(t)

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// Simulate a tx context via CommitHook with TxBypass (default).
	hook := &fakeHook{}
	txDB := db.Set(CommitHookKey, CommitHook(hook))

	var u testUser
	require.NoError(t, txDB.First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)
	assert.Equal(t, 0, met.hits, "TxBypass should skip cache reads")
	assert.Equal(t, 0, met.misses, "TxBypass should skip cache entirely")
}

func TestCallback_TxDeferred_DefersCacheSet(t *testing.T) {
	db, mc, _ := setupCachedDB(t, WithDefaultTxMode(TxDeferred))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	hook := &fakeHook{}
	txDB := db.Set(CommitHookKey, CommitHook(hook))

	var u testUser
	require.NoError(t, txDB.First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)

	// Cache should not be populated yet (deferred to commit).
	assert.Equal(t, 0, mc.Len(), "TxDeferred should not set cache inline")
	assert.Len(t, hook.commits, 1, "should have registered one commit callback")

	// Simulate commit.
	for _, fn := range hook.commits {
		require.NoError(t, fn(context.Background()))
	}
	assert.Equal(t, 1, mc.Len(), "cache should be populated after commit")
}

func TestCallback_TxDeferred_DefersInvalidation(t *testing.T) {
	db, mc, _ := setupCachedDB(t, WithDefaultTxMode(TxDeferred))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// Populate cache.
	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, 1, mc.Len())

	// Write inside a deferred tx should defer invalidation.
	hook := &fakeHook{}
	txDB := db.Set(CommitHookKey, CommitHook(hook))
	require.NoError(t, txDB.Model(&testUser{ID: 1}).Update("name", "bob").Error)

	// Cache should still have the entry (invalidation deferred).
	assert.Equal(t, 1, mc.Len(), "deferred invalidation should not clear cache yet")

	// Simulate commit.
	for _, fn := range hook.commits {
		require.NoError(t, fn(context.Background()))
	}
	assert.Equal(t, 0, mc.Len(), "cache should be cleared after commit")
}

func TestCallback_PendingTracker_SkipsCache(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// Populate cache.
	var u1 testUser
	require.NoError(t, db.First(&u1, 1).Error)
	assert.Equal(t, 1, mc.Len())
	assert.Equal(t, 1, met.misses)

	// With pending tracker indicating dirty state → skip cache.
	pt := &fakePendingTracker{pending: true}
	dirtyDB := db.Set(PendingTrackerKey, PendingTracker(pt))

	var u2 testUser
	require.NoError(t, dirtyDB.First(&u2, 1).Error)
	assert.Equal(t, "alice", u2.Name)
	// Should not have gotten a cache hit (dirty state bypasses cache).
	assert.Equal(t, 1, met.misses, "dirty-state query should not count as miss in cache path")
}

func TestCallback_CountRows_Slice(t *testing.T) {
	assert.Equal(t, int64(0), countRows(nil))
	assert.Equal(t, int64(1), countRows(&testUser{}))

	users := []testUser{{}, {}, {}}
	assert.Equal(t, int64(3), countRows(&users))
}

// --- helpers ---

type fakePendingTracker struct {
	pending bool
}

func (f *fakePendingTracker) MarkPending()        { f.pending = true }
func (f *fakePendingTracker) HasPendingWrites() bool { return f.pending }

func extractPlugin(t *testing.T, db *gorm.DB) *Plugin {
	t.Helper()
	raw, ok := db.Config.Plugins[pluginName]
	require.True(t, ok, "plugin not registered")
	p, ok := raw.(*Plugin)
	require.True(t, ok)
	return p
}
