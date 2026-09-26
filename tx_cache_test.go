package gormrepository

import (
	"context"
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

func setupTxCacheDB(t *testing.T) (*gorm.DB, *GormRepository[txCacheTestWidget]) {
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

	p := plugin.New(memory.New(),
		plugin.WithDefaultTTL(5*time.Minute),
		plugin.WithDefaultTxMode(plugin.TxDeferred),
	)
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
