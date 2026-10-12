package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// testUser is a simple model for callback tests.
type testUser struct {
	ID   uint `gorm:"primarykey"`
	Name string
}

func (testUser) TableName() string { return "test_users" }

// cachedTestUser implements CacheTaggable for invalidation tests.
type cachedTestUser struct {
	ID   uint `gorm:"primarykey"`
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
	hits             int
	misses           int
	invalidations    int
	postCommitErrors int
	lastModel        string
	lastReason       string
}

func (m *testMetrics) Hit(model string)  { m.hits++; m.lastModel = model }
func (m *testMetrics) Miss(model string) { m.misses++; m.lastModel = model }
func (m *testMetrics) Invalidation(model, reason string) {
	m.invalidations++
	m.lastModel = model
	m.lastReason = reason
}
func (m *testMetrics) PostCommitError() { m.postCommitErrors++ }

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

// TestCallback_TxDeferred_DoesNotStoreTxReads: a value read inside a
// transaction can be overtaken by another transaction's commit before this
// one ends, so TxDeferred never stores it — neither inline nor at commit.
func TestCallback_TxDeferred_DoesNotStoreTxReads(t *testing.T) {
	db, mc, _ := setupCachedDB(t, WithDefaultTxMode(TxDeferred))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	hook := &fakeHook{}
	txDB := db.Set(CommitHookKey, CommitHook(hook))

	var u testUser
	require.NoError(t, txDB.First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)

	assert.Equal(t, 0, mc.Len(), "TxDeferred must not store a read made inside the tx")
	assert.Empty(t, hook.commits, "nor defer storing it to commit")
}

// TestCallback_TxDeferred_ServesCachedValueInTx: a value cached outside
// the tx may still be served inside it, until the tx writes.
func TestCallback_TxDeferred_ServesCachedValueInTx(t *testing.T) {
	db, _, met := setupCachedDB(t, WithDefaultTxMode(TxDeferred))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	var warm testUser
	require.NoError(t, db.First(&warm, 1).Error)

	txDB := db.Set(CommitHookKey, CommitHook(&fakeHook{}))
	var u testUser
	require.NoError(t, txDB.First(&u, 1).Error)
	assert.Equal(t, "alice", u.Name)
	assert.Equal(t, 1, met.hits)
}

// TestCallback_TxFull_StoresTxReadsInline: TxFull stores a read made
// inside the tx right away, but still defers its writes' invalidation.
func TestCallback_TxFull_StoresTxReadsInline(t *testing.T) {
	db, mc, _ := setupCachedDB(t, WithDefaultTxMode(TxFull))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	hook := &fakeHook{}
	// Session: each call below gets its own Statement.
	txDB := db.Set(CommitHookKey, CommitHook(hook)).Session(&gorm.Session{})
	var u testUser
	require.NoError(t, txDB.First(&u, 1).Error)
	assert.Equal(t, 1, mc.Len(), "TxFull stores the read inline")

	require.NoError(t, txDB.Model(&testUser{ID: 1}).Update("name", "bob").Error)
	require.Len(t, hook.commits, 1)
	assert.Equal(t, 1, mc.Len(), "the write's invalidation waits for commit")
	require.NoError(t, hook.commits[0](context.Background()))
	assert.Equal(t, 0, mc.Len())
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

// fakeSQLLogger records every Trace call gorm's processor makes — the
// call that logs stmt.SQL after the query callback chain runs, regardless
// of whether a real query executed.
type fakeSQLLogger struct {
	logger.Interface
	traces int
}

func (f *fakeSQLLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	f.traces++
}

func TestCallback_QueryHit_DoesNotLogSQL(t *testing.T) {
	fakeLog := &fakeSQLLogger{Interface: logger.Default}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: fakeLog})
	require.NoError(t, err)

	mc := memory.New()
	require.NoError(t, db.Use(New(mc, WithDefaultTTL(5*time.Minute))))
	require.NoError(t, db.AutoMigrate(&testUser{}))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	// First query: cache miss → real query → SQL trace expected.
	fakeLog.traces = 0
	var u1 testUser
	require.NoError(t, db.First(&u1, 1).Error)
	assert.Equal(t, 1, fakeLog.traces, "a real query should still be traced")

	// Second, identical query: cache hit → no real query → no SQL trace,
	// even though BuildQuerySQL still runs to derive the cache key.
	fakeLog.traces = 0
	var u2 testUser
	require.NoError(t, db.First(&u2, 1).Error)
	assert.Equal(t, "alice", u2.Name)
	assert.Equal(t, 0, fakeLog.traces, "a cache hit must not log a SELECT that never ran")
}

// TestCallback_AnonymousStructDest_NeverCached guards the fix for a real
// GORM behavior: the anonymous struct type GORM builds on the fly for a
// many2many join table copies each foreign-key field's ENTIRE original
// struct tag from the related model's primary-key field — json tag
// included. Since almost every model's primary key is tagged json:"id",
// two such foreign-key fields collide on that same tag, and
// encoding/json's documented behavior is to silently omit BOTH fields —
// so a cached entry here would have no real data in it, and a cache hit
// would hand back zero-value foreign keys. Rather than work around that
// serialization quirk, an anonymous dest type is simply never cached (see
// isAnonymousStructDest) — cheap, indexed pivot-table lookups don't need
// it, and it removes the risk entirely instead of papering over it.
func TestCallback_AnonymousStructDest_NeverCached(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	for i := 1; i <= 2; i++ {
		var rows []struct {
			ID   uint
			Name string
		}
		require.NoError(t, db.Table("test_users").Find(&rows).Error)
		require.Len(t, rows, 1)
		assert.Equal(t, "alice", rows[0].Name)
	}

	assert.Equal(t, 0, mc.Len(), "an anonymous struct dest must never be cached")
	assert.Equal(t, 0, met.hits)
	assert.Equal(t, 0, met.misses, "skipped entirely, not even counted as a miss")
}

// TestCallback_NamedStructDest_StillCached is the control for the test
// above: a normal, named struct type continues to cache exactly as before.
func TestCallback_NamedStructDest_StillCached(t *testing.T) {
	db, mc, met := setupCachedDB(t)

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	var u1 testUser
	require.NoError(t, db.First(&u1, 1).Error)
	var u2 testUser
	require.NoError(t, db.First(&u2, 1).Error)

	assert.Equal(t, 1, mc.Len())
	assert.Equal(t, 1, met.misses)
	assert.Equal(t, 1, met.hits)
}

// A grouped Count scans one row per group into a single *int64 and
// reports the number of groups through RowsAffected. The hit used to guess
// RowsAffected from Dest (always 1), so it counted one group.
func TestCallback_GroupedCount_HitMatchesMiss(t *testing.T) {
	db, _, met := setupCachedDB(t)
	for i, name := range []string{"a", "b", "b", "c"} {
		require.NoError(t, db.Create(&testUser{ID: uint(i + 1), Name: name}).Error)
	}

	var groupsMiss, groupsHit, totalMiss, totalHit int64
	require.NoError(t, db.Model(&testUser{}).Group("name").Count(&groupsMiss).Error)
	require.NoError(t, db.Model(&testUser{}).Group("name").Count(&groupsHit).Error)
	require.NoError(t, db.Model(&testUser{}).Count(&totalMiss).Error)
	require.NoError(t, db.Model(&testUser{}).Count(&totalHit).Error)
	require.Equal(t, 2, met.hits)
	assert.Equal(t, int64(3), groupsMiss)
	assert.Equal(t, groupsMiss, groupsHit)
	assert.Equal(t, int64(4), totalMiss)
	assert.Equal(t, totalMiss, totalHit)

	var miss, hit []testUser
	resultMiss, resultHit := gorm.WithResult(), gorm.WithResult()
	resMiss := db.Clauses(resultMiss).Where("name = ?", "b").Find(&miss)
	resHit := db.Clauses(resultHit).Where("name = ?", "b").Find(&hit)
	require.Equal(t, 3, met.hits)
	assert.Equal(t, int64(2), resHit.RowsAffected)
	assert.Equal(t, resMiss.RowsAffected, resHit.RowsAffected)
	assert.Equal(t, int64(2), resultHit.RowsAffected, "gorm.WithResult sees it too")
	assert.Equal(t, resultMiss.RowsAffected, resultHit.RowsAffected)
}

// A DryRun (or ToSQL) only builds the SQL. It used to be answered from
// the cache, which also cleared that SQL.
func TestCallback_DryRun_NeverServedFromCache(t *testing.T) {
	db, sc, _, _, met := setupScriptedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)
	var warm testUser
	require.NoError(t, db.First(&warm, 1).Error)
	gets := sc.gets

	var u testUser
	res := db.Session(&gorm.Session{DryRun: true}).First(&u, 1)
	require.NoError(t, res.Error)
	assert.Contains(t, res.Statement.SQL.String(), "test_users")
	assert.Zero(t, u.ID, "a DryRun returns no rows")

	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		var x testUser
		return tx.First(&x, 1)
	})
	assert.Contains(t, sql, "test_users")
	assert.Zero(t, met.hits)
	assert.Equal(t, gets, sc.gets, "a DryRun never looks the key up")
}

// noWriteTagsStrategy derives read tags as usual but no write tags at all.
type noWriteTagsStrategy struct{}

func (noWriteTagsStrategy) ReadTags(db *gorm.DB, _ string) []string {
	return []string{"table:" + db.Statement.Table}
}

func (noWriteTagsStrategy) WriteTags(*gorm.DB, string) []string { return nil }

// A write that derives no tags has nothing to invalidate, but it still
// dirties the transaction: it used to return before MarkPending.
func TestCallback_TaglessWrite_MarksPending(t *testing.T) {
	db, _, _ := setupCachedDB(t, WithDefaultTxMode(TxDeferred), WithTagStrategy(noWriteTagsStrategy{}))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)

	pt := &fakePendingTracker{}
	tx := db.Set(CommitHookKey, CommitHook(&fakeHook{})).Set(PendingTrackerKey, PendingTracker(pt))
	require.NoError(t, tx.Model(&testUser{ID: 1}).Update("name", "b").Error)
	assert.True(t, pt.pending)
}

// A raw write whose table can't be parsed (here behind a CTE) can't be
// invalidated, but it is reported and still dirties the transaction.
func TestCallback_ExecUnknownTable_MarksPending(t *testing.T) {
	db, _, _, rec, _ := setupScriptedDB(t, WithDefaultTxMode(TxDeferred))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)

	pt := &fakePendingTracker{}
	// Session: each call below gets its own Statement.
	tx := db.Set(CommitHookKey, CommitHook(&fakeHook{})).Set(PendingTrackerKey, PendingTracker(pt)).Session(&gorm.Session{})
	require.NoError(t, tx.Exec("WITH x AS (SELECT 1) UPDATE test_users SET name = 'b' WHERE id = 1").Error)
	assert.True(t, pt.pending)
	assert.True(t, rec.hasSkip("exec-unknown-table"))

	// A CTE that only reads is neither.
	pt.pending = false
	rec.reset()
	var n int
	require.NoError(t, tx.Raw("WITH x AS (SELECT 1 AS n) SELECT n FROM x").Scan(&n).Error)
	assert.Equal(t, 1, n)
	assert.False(t, pt.pending)
	assert.False(t, rec.hasSkip("exec-unknown-table"))
}

// A failed query is never stored.
func TestCallback_QueryError_NotCached(t *testing.T) {
	db, _, mc, rec, _ := setupScriptedDB(t)

	for i := 0; i < 2; i++ {
		var users []testUser
		require.Error(t, db.Where("no_such_column = ?", 1).Find(&users).Error)
	}
	assert.Equal(t, 0, mc.Len())
	assert.Len(t, rec.kindsOf(EventMiss), 2, "the error was never cached")
	skip := rec.last(EventSkip)
	assert.Equal(t, "query-error", skip.Reason)
	assert.Error(t, skip.Err)
}

// A read whose result arrives more than maxReadAge after its lookup isn't
// stored: the backend may no longer remember the invalidations since.
func TestCallback_SlowRead_NotStored(t *testing.T) {
	db, _, mc, rec, _ := setupScriptedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)

	base := time.Now()
	calls := 0
	nowFunc = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(maxReadAge + time.Second)
	}
	t.Cleanup(func() { nowFunc = time.Now })

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "a", u.Name)
	assert.True(t, rec.hasSkip("slow-read"))
	assert.Equal(t, 0, mc.Len())
}

// A write that reached the database invalidates even if the request's
// context is cancelled before the invalidation runs, inline or at commit.
func TestCallback_InvalidationSurvivesCancelledContext(t *testing.T) {
	t.Run("inline", func(t *testing.T) {
		db, sc, mc, _, _ := setupScriptedDB(t)
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)
		var warm testUser
		require.NoError(t, db.First(&warm, 1).Error)
		require.Equal(t, 1, mc.Len())

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Cancel after the write committed, before the invalidation runs.
		require.NoError(t, db.Callback().Update().After(commitOrRollbackName).Before(updateAfterName).
			Register("test:cancel", func(*gorm.DB) { cancel() }))

		require.NoError(t, db.WithContext(ctx).Model(&testUser{ID: 1}).Update("name", "b").Error)
		require.Error(t, ctx.Err())
		assert.NoError(t, sc.invalidateCtxErr, "the invalidation must not inherit the cancellation")
		assert.Equal(t, 0, mc.Len())
	})

	t.Run("deferred", func(t *testing.T) {
		db, sc, mc, _, met := setupScriptedDB(t)
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)
		var warm testUser
		require.NoError(t, db.First(&warm, 1).Error)

		hook := &fakeHook{}
		require.NoError(t, db.Set(CommitHookKey, CommitHook(hook)).Model(&testUser{ID: 1}).Update("name", "b").Error)
		require.Len(t, hook.commits, 1)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		require.NoError(t, hook.commits[0](ctx))
		assert.NoError(t, sc.invalidateCtxErr)
		assert.Equal(t, 0, mc.Len())
		assert.Zero(t, met.postCommitErrors)
	})
}

// The payload carries the miss's RowsAffected, which the hit reports
// as-is instead of guessing it from Dest.
func TestCodec_RowsAffectedRoundTrips(t *testing.T) {
	for _, rows := range []int64{0, 1, 3, 1 << 40} {
		data, err := encode(&[]testUser{{}, {}, {}}, rows)
		require.NoError(t, err)
		var out []testUser
		found, got, err := decode(data, &out)
		require.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, rows, got)
		assert.Len(t, out, 3)
	}

	data, err := encode(&testUser{}, -1)
	require.NoError(t, err)
	_, got, err := decode(data, &testUser{})
	require.NoError(t, err)
	assert.Zero(t, got, "a negative count is stored as 0")
}

// --- helpers ---

// scriptedCache wraps a Cache, counting calls, and lets a test fail them
// or run code between a read's Get and its Set.
type scriptedCache struct {
	Cache
	mu            sync.Mutex
	gets, sets    int
	invalidations int
	getErr        error
	setErr        error
	invalidateErr error
	// beforeSet runs before each Set reaches the wrapped cache.
	beforeSet func(ctx context.Context, tags []string)
	// lastGetVersions / lastSetVersions record what Get returned and what
	// Set was handed back.
	lastGetVersions Versions
	lastSetVersions Versions
	// invalidateCtxErr is ctx.Err() as the last Invalidate saw it.
	invalidateCtxErr error
}

func (c *scriptedCache) Get(ctx context.Context, key string, tags []string) ([]byte, bool, Versions, error) {
	c.mu.Lock()
	c.gets++
	err := c.getErr
	c.mu.Unlock()
	if err != nil {
		return nil, false, nil, err
	}
	data, hit, versions, err := c.Cache.Get(ctx, key, tags)
	c.mu.Lock()
	c.lastGetVersions = versions
	c.mu.Unlock()
	return data, hit, versions, err
}

func (c *scriptedCache) Set(ctx context.Context, key string, value []byte, tags []string, ttl time.Duration, versions Versions) error {
	c.mu.Lock()
	c.sets++
	c.lastSetVersions = versions
	err, before := c.setErr, c.beforeSet
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if before != nil {
		before(ctx, tags)
	}
	return c.Cache.Set(ctx, key, value, tags, ttl, versions)
}

func (c *scriptedCache) Invalidate(ctx context.Context, tags []string) error {
	c.mu.Lock()
	c.invalidations++
	c.invalidateCtxErr = ctx.Err()
	err := c.invalidateErr
	c.mu.Unlock()
	if err != nil {
		return err
	}
	return c.Cache.Invalidate(ctx, tags)
}

// setupScriptedDB is setupCachedDB over a scriptedCache wrapping a memory
// cache, with events recorded.
func setupScriptedDB(t *testing.T, opts ...Option) (*gorm.DB, *scriptedCache, *memory.Cache, *testRecorder, *testMetrics) {
	t.Helper()
	mc := memory.New()
	sc := &scriptedCache{Cache: mc}
	met := &testMetrics{}
	rec := &testRecorder{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Use(New(sc, append([]Option{WithDefaultTTL(5 * time.Minute), WithMetrics(met), WithEventRecorder(rec)}, opts...)...)))
	require.NoError(t, db.AutoMigrate(&testUser{}, &cachedTestUser{}))
	return db, sc, mc, rec, met
}

type fakePendingTracker struct {
	pending bool
}

func (f *fakePendingTracker) MarkPending()           { f.pending = true }
func (f *fakePendingTracker) HasPendingWrites() bool { return f.pending }

func extractPlugin(t *testing.T, db *gorm.DB) *Plugin {
	t.Helper()
	raw, ok := db.Config.Plugins[pluginName]
	require.True(t, ok, "plugin not registered")
	p, ok := raw.(*Plugin)
	require.True(t, ok)
	return p
}
