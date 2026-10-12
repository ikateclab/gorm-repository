package gormrepository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ikateclab/gorm-repository/plugin"
	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// txCacheTestWidget is a minimal model for the TxDeferred + PendingTracker
// correctness tests below — independent of tests.TestUser, which uses
// Postgres-only column types (jsonb/timestamptz) that don't matter here.
type txCacheTestWidget struct {
	Id   uuid.UUID `gorm:"primarykey"`
	Name string
}

func (txCacheTestWidget) TableName() string { return "tx_cache_test_widgets" }

// Clone and Diff satisfy Diffable[txCacheTestWidget], required by
// UpdateById when called WithTx (see gorm_repository.go's diff-tracking
// path for in-transaction updates).
func (w *txCacheTestWidget) Clone() *txCacheTestWidget {
	clone := *w
	return &clone
}

func (w *txCacheTestWidget) Diff(other *txCacheTestWidget) map[string]interface{} {
	diff := map[string]interface{}{}
	if w.Name != other.Name {
		diff["name"] = w.Name
	}
	return diff
}

func setupTxCacheDB(t *testing.T, opts ...plugin.Option) (*gorm.DB, *GormRepository[txCacheTestWidget]) {
	t.Helper()
	return openTxCacheDB(t, memory.New(), opts...)
}

// openTxCacheDB is setupTxCacheDB over the given cache backend.
func openTxCacheDB(t *testing.T, cache plugin.Cache, opts ...plugin.Option) (*gorm.DB, *GormRepository[txCacheTestWidget]) {
	t.Helper()
	// "cache=shared" (rather than forcing MaxOpenConns(1)) lets every
	// connection in the pool see the same :memory: database — pinning the
	// pool to a single connection instead deadlocks here: this test holds
	// an open transaction's connection while issuing a plain, untransacted
	// query for the still-cached pre-commit value, which would otherwise
	// have no connection left to use. The DSN is named per-test (rather
	// than the bare "file::memory:") because "cache=shared" makes any two
	// connections using the SAME name share one process-wide database —
	// without a unique name, every test in this file would collide on one
	// shared table.
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)

	p := plugin.New(cache, append([]plugin.Option{
		plugin.WithDefaultTTL(5 * time.Minute),
		plugin.WithDefaultTxMode(plugin.TxDeferred),
	}, opts...)...)
	require.NoError(t, db.Use(p))
	require.NoError(t, db.AutoMigrate(&txCacheTestWidget{}))

	return db, &GormRepository[txCacheTestWidget]{DB: db}
}

// TestTxDeferred_ReadAfterWriteInSameTx_NeverStale is the correctness
// property the whole PendingTracker mechanism exists for: under TxDeferred,
// a write's cache invalidation is deferred to commit, so a read of the same
// row later in the same transaction must not be served the value cached
// before the write — MarkPending/HasPendingWrites force that read to miss
// the cache and go to the DB instead, where it sees its own transaction's
// uncommitted write.
func TestTxDeferred_ReadAfterWriteInSameTx_NeverStale(t *testing.T) {
	_, repo := setupTxCacheDB(t)
	ctx := context.Background()

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: id, Name: "before"}))

	// Populate the cache with the pre-transaction value.
	cached, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "before", cached.Name)

	tx := repo.BeginTransaction()

	err = repo.UpdateById(ctx, id, &txCacheTestWidget{Id: id, Name: "after"}, WithTx(tx))
	require.NoError(t, err)

	// Same transaction, same row: must see "after", not the cached "before".
	reread, err := repo.FindById(ctx, id, WithTx(tx))
	require.NoError(t, err)
	assert.Equal(t, "after", reread.Name, "a read after a write in the same TxDeferred transaction must not be served the pre-write cached value")

	require.NoError(t, tx.Commit())

	final, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "after", final.Name)
}

// TestTxDeferred_InvalidatesOnlyAfterCommit confirms the other half of
// TxDeferred: the cache entry populated before the transaction started is
// NOT invalidated until the transaction actually commits.
func TestTxDeferred_InvalidatesOnlyAfterCommit(t *testing.T) {
	_, repo := setupTxCacheDB(t)
	ctx := context.Background()

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: id, Name: "before"}))

	_, err := repo.FindById(ctx, id)
	require.NoError(t, err)

	tx := repo.BeginTransaction()
	require.NoError(t, repo.UpdateById(ctx, id, &txCacheTestWidget{Id: id, Name: "after"}, WithTx(tx)))

	// Outside the transaction, the cache entry from before the write is
	// still untouched — invalidation hasn't happened yet.
	stillCached, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "before", stillCached.Name, "TxDeferred must not invalidate before commit")

	require.NoError(t, tx.Commit())

	afterCommit, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "after", afterCommit.Name, "commit must invalidate the pre-transaction cache entry")
}

// TestTxDeferred_RollbackNeverInvalidates confirms a rolled-back
// transaction's write never reaches the cache — the pre-transaction value
// stays cached and correct.
func TestTxDeferred_RollbackNeverInvalidates(t *testing.T) {
	_, repo := setupTxCacheDB(t)
	ctx := context.Background()

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: id, Name: "before"}))

	_, err := repo.FindById(ctx, id)
	require.NoError(t, err)

	tx := repo.BeginTransaction()
	require.NoError(t, repo.UpdateById(ctx, id, &txCacheTestWidget{Id: id, Name: "after"}, WithTx(tx)))
	require.NoError(t, tx.Rollback())

	stillCached, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "before", stillCached.Name)
}

// TestTxBypass_WriteInTx_InvalidatesOnlyAfterCommit covers the default
// mode: a write inside the repository's Tx used to invalidate inline,
// before commit, so a concurrent reader could re-cache the old row and
// keep serving it after the commit.
func TestTxBypass_WriteInTx_InvalidatesOnlyAfterCommit(t *testing.T) {
	_, repo := setupTxCacheDB(t, plugin.WithDefaultTxMode(plugin.TxBypass))
	ctx := context.Background()

	id := uuid.New()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: id, Name: "before"}))
	_, err := repo.FindById(ctx, id)
	require.NoError(t, err)

	tx := repo.BeginTransaction()
	require.NoError(t, repo.UpdateById(ctx, id, &txCacheTestWidget{Id: id, Name: "after"}, WithTx(tx)))

	stillCached, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "before", stillCached.Name, "TxBypass must defer invalidation to commit too")

	require.NoError(t, tx.Commit())

	afterCommit, err := repo.FindById(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "after", afterCommit.Name)
}

// failingInvalidations is a cache whose invalidations always fail.
type failingInvalidations struct{ plugin.Cache }

func (failingInvalidations) Invalidate(context.Context, []string) error {
	return errors.New("cache down")
}

// postCommitMetrics counts PostCommitError calls.
type postCommitMetrics struct {
	plugin.Metrics
	postCommitErrors int
}

func (m *postCommitMetrics) PostCommitError() { m.postCommitErrors++ }

// The commit already happened when its invalidation fails: Commit still
// succeeds, and the failure is counted.
func TestTx_PostCommitError_CommitStillSucceeds(t *testing.T) {
	met := &postCommitMetrics{Metrics: plugin.New(nil).Metrics()}
	_, repo := openTxCacheDB(t, failingInvalidations{memory.New()}, plugin.WithMetrics(met))
	ctx := context.Background()

	tx := repo.BeginTransaction()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: uuid.New(), Name: "a"}, WithTx(tx)))
	assert.Zero(t, met.postCommitErrors, "deferred until commit")
	require.NoError(t, tx.Commit())
	assert.Equal(t, 1, met.postCommitErrors)
}

// idHintStrategy records the IDHint each write carries.
type idHintStrategy struct{ hints []string }

func (s *idHintStrategy) ReadTags(db *gorm.DB, _ string) []string {
	return []string{"table:" + db.Statement.Table}
}

func (s *idHintStrategy) WriteTags(db *gorm.DB, _ string) []string {
	tags := []string{"table:" + db.Statement.Table}
	if id, ok := plugin.IDHint(db); ok {
		s.hints = append(s.hints, id)
		tags = append(tags, "widget:"+id)
	}
	return tags
}

// DeleteById and UpdateByIdWithMap leave Dest blank, so they hand the id
// they were given to a TagStrategy through IDHint.
func TestRepository_IDHint_FromDeleteByIdAndUpdateByIdWithMap(t *testing.T) {
	strategy := &idHintStrategy{}
	_, repo := setupTxCacheDB(t, plugin.WithTagStrategy(strategy))
	ctx := context.Background()

	updated, deleted := uuid.New(), uuid.New()
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: updated, Name: "a"}))
	require.NoError(t, repo.Create(ctx, &txCacheTestWidget{Id: deleted, Name: "b"}))
	assert.Empty(t, strategy.hints, "Create has its entity, and no hint")

	_, err := repo.UpdateByIdWithMap(ctx, updated, map[string]interface{}{"name": "a2"})
	require.NoError(t, err)
	require.NoError(t, repo.DeleteById(ctx, deleted))
	assert.Equal(t, []string{updated.String(), deleted.String()}, strategy.hints)
}

func TestTx_HookLifecycle(t *testing.T) {
	_, repo := setupTxCacheDB(t)
	var order []string
	record := func(name string) func(context.Context) error {
		return func(context.Context) error { order = append(order, name); return nil }
	}

	t.Run("commit runs commit hooks once, in order", func(t *testing.T) {
		order = nil
		tx := repo.BeginTransaction()
		require.NoError(t, tx.Error())
		tx.OnCommit(record("first"))
		tx.OnCommit(record("second"))
		tx.OnRollback(func() { order = append(order, "rollback") })
		require.NoError(t, tx.Commit())
		require.NoError(t, tx.Commit(), "a second commit is a no-op")
		require.NoError(t, tx.Rollback(), "so is a rollback after it")
		assert.Equal(t, []string{"first", "second"}, order)
	})

	t.Run("rollback runs rollback hooks only", func(t *testing.T) {
		order = nil
		tx := repo.BeginTransaction()
		tx.OnCommit(record("commit"))
		tx.OnRollback(func() { order = append(order, "rollback") })
		require.NoError(t, tx.Rollback())
		require.NoError(t, tx.Commit(), "a commit after rollback is a no-op")
		assert.Equal(t, []string{"rollback"}, order)
	})

	t.Run("Finish commits on nil error", func(t *testing.T) {
		order = nil
		var err error
		tx := repo.BeginTransaction()
		tx.OnCommit(record("commit"))
		tx.Finish(&err)
		require.NoError(t, err)
		assert.True(t, tx.committed)
		tx.Finish(&err)
		assert.Equal(t, []string{"commit"}, order, "Finish after commit is a no-op")
	})

	t.Run("Finish rolls back on error", func(t *testing.T) {
		order = nil
		err := errors.New("boom")
		tx := repo.BeginTransaction()
		tx.OnRollback(func() { order = append(order, "rollback") })
		tx.Finish(&err)
		assert.EqualError(t, err, "boom", "the original error is kept")
		assert.True(t, tx.rolledBack)
		assert.Equal(t, []string{"rollback"}, order)
	})

	t.Run("Finish reports a failed commit", func(t *testing.T) {
		order = nil
		var err error
		tx := repo.BeginTransaction()
		tx.OnCommit(record("commit"))
		require.NoError(t, tx.gtx.Commit().Error) // finished behind the Tx's back
		tx.Finish(&err)
		assert.Error(t, err)
		assert.False(t, tx.committed)
		assert.Empty(t, order, "hooks don't run when the commit fails")
	})

	t.Run("a failed rollback runs no hooks", func(t *testing.T) {
		order = nil
		tx := repo.BeginTransaction()
		tx.OnRollback(func() { order = append(order, "rollback") })
		require.NoError(t, tx.gtx.Rollback().Error)
		err := errors.New("boom")
		tx.Finish(&err)
		assert.EqualError(t, err, "boom")
		assert.False(t, tx.rolledBack)
		assert.Empty(t, order)
	})
}
