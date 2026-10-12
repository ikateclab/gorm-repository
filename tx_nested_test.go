package gormrepository

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin"
	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// nestedTxWidget is the model for the nested-transaction tests, which run
// on SQLite and Postgres alike (hence the explicit uuid column type).
type nestedTxWidget struct {
	Id   uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name string
}

func (nestedTxWidget) TableName() string { return "nested_tx_widgets" }

type nestedTxEnv struct {
	db   *gorm.DB
	repo *GormRepository[nestedTxWidget]
	ctx  context.Context
}

// create inserts a widget and warms the cache with it.
func (e nestedTxEnv) create(t *testing.T, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	require.NoError(t, e.repo.Create(e.ctx, &nestedTxWidget{Id: id, Name: name}))
	got, err := e.repo.FindById(e.ctx, id)
	require.NoError(t, err)
	require.Equal(t, name, got.Name)
	return id
}

// name reads a widget through the cache.
func (e nestedTxEnv) name(t *testing.T, id uuid.UUID, opts ...Option) string {
	t.Helper()
	got, err := e.repo.FindById(e.ctx, id, opts...)
	require.NoError(t, err)
	return got.Name
}

// stored reads a widget from the database, past the cache.
func (e nestedTxEnv) stored(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var w nestedTxWidget
	require.NoError(t, plugin.Bypass(e.db).First(&w, "id = ?", id).Error)
	return w.Name
}

func (e nestedTxEnv) rename(t *testing.T, id uuid.UUID, name string, tx *Tx) error {
	t.Helper()
	_, err := e.repo.UpdateByIdWithMap(e.ctx, id, map[string]interface{}{"name": name}, WithTx(tx))
	return err
}

func newNestedTxEnv(t *testing.T, dialector gorm.Dialector, prepareStmt bool) nestedTxEnv {
	t.Helper()
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Discard, PrepareStmt: prepareStmt})
	require.NoError(t, err)
	require.NoError(t, db.Use(plugin.New(memory.New(),
		plugin.WithDefaultTTL(5*time.Minute),
		plugin.WithDefaultTxMode(plugin.TxDeferred),
	)))
	require.NoError(t, db.AutoMigrate(&nestedTxWidget{}))
	return nestedTxEnv{db: db, repo: &GormRepository[nestedTxWidget]{DB: db}, ctx: context.Background()}
}

// forEachNestedTxDB runs fn against SQLite (WAL, so reads outside an open
// transaction see the committed state, as on Postgres) and against the
// package's Postgres container.
func forEachNestedTxDB(t *testing.T, fn func(t *testing.T, e nestedTxEnv)) {
	forEachNestedTxDBPrepared(t, false, fn)
}

func forEachNestedTxDBPrepared(t *testing.T, prepareStmt bool, fn func(t *testing.T, e nestedTxEnv)) {
	t.Run("sqlite", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "nested.db") + "?_journal_mode=WAL&_busy_timeout=5000"
		fn(t, newNestedTxEnv(t, sqlite.Open(dsn), prepareStmt))
	})
	t.Run("postgres", func(t *testing.T) {
		sqlDB, err := testDB.DB()
		require.NoError(t, err)
		fn(t, newNestedTxEnv(t, postgres.New(postgres.Config{Conn: sqlDB}), prepareStmt))
	})
}

// A nested transaction used to call Begin on the outer *gorm.DB, which
// fails inside a transaction: the child was unusable, and committing it
// broke the parent, whose invalidation never ran. Now it's a savepoint
// whose hooks run only when the outermost transaction commits.
func TestNestedTx_ChildCommit_InvalidatesOnOuterCommit(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		child := parent.BeginTransaction()
		require.NoError(t, child.Error())
		require.NoError(t, e.rename(t, id, "after", child))
		require.NoError(t, child.Commit())
		assert.Equal(t, "before", e.name(t, id), "nothing is invalidated before the outer commit")

		require.NoError(t, parent.Commit())
		assert.Equal(t, "after", e.stored(t, id))
		assert.Equal(t, "after", e.name(t, id))
	})
}

// Rolling a child back undoes its writes alone, runs its rollback hooks
// and drops its commit hooks; the parent carries on and commits.
func TestNestedTx_ChildRollback_UndoesOnlyTheChild(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		a := e.create(t, "a0")
		b := e.create(t, "b0")

		parent := e.repo.BeginTransaction()
		require.NoError(t, e.rename(t, a, "a1", parent))
		child := parent.BeginTransaction()
		require.NoError(t, e.rename(t, b, "b1", child))
		rolledBack, committed := false, false
		child.OnRollback(func() { rolledBack = true })
		child.OnCommit(func(context.Context) error { committed = true; return nil })

		require.NoError(t, child.Rollback())
		assert.True(t, rolledBack)
		assert.Equal(t, "b0", e.name(t, b, WithTx(parent)), "the child's write is undone")
		require.NoError(t, e.rename(t, b, "b2", parent), "the parent carries on")

		require.NoError(t, parent.Commit())
		assert.False(t, committed, "a rolled-back child's commit hooks never run")
		for id, want := range map[uuid.UUID]string{a: "a1", b: "b2"} {
			assert.Equal(t, want, e.stored(t, id))
			assert.Equal(t, want, e.name(t, id))
		}
	})
}

// A statement that fails inside a child (which aborts the whole
// transaction on Postgres) is recovered by rolling back to its savepoint.
func TestNestedTx_FailedStatementRecoveredBySavepoint(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		a := e.create(t, "a0")
		b := e.create(t, "b0")

		parent := e.repo.BeginTransaction()
		require.NoError(t, e.rename(t, a, "a1", parent))
		child := parent.BeginTransaction()
		require.Error(t, e.repo.Create(e.ctx, &nestedTxWidget{Id: a, Name: "duplicate"}, WithTx(child)))
		require.NoError(t, child.Rollback())

		require.NoError(t, e.rename(t, b, "b1", parent))
		require.NoError(t, parent.Commit())
		assert.Equal(t, "a1", e.name(t, a))
		assert.Equal(t, "b1", e.name(t, b))
	})
}

// A write in a child dirties its parent, and one in the parent dirties
// the child: both see it, so neither may be served the cached value.
func TestNestedTx_PendingWritesSeenAcrossLevels(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		child := parent.BeginTransaction()
		require.NoError(t, e.rename(t, id, "by-child", child))
		assert.True(t, parent.HasPendingWrites())
		assert.Equal(t, "by-child", e.name(t, id, WithTx(parent)))
		require.NoError(t, child.Commit())
		require.NoError(t, parent.Commit())

		other := e.create(t, "before")
		parent = e.repo.BeginTransaction()
		child = parent.BeginTransaction()
		require.NoError(t, e.rename(t, other, "by-parent", parent))
		assert.True(t, child.HasPendingWrites())
		assert.Equal(t, "by-parent", e.name(t, other, WithTx(child)))
		require.NoError(t, child.Commit())
		require.NoError(t, parent.Commit())
	})
}

// The child's hook and tracker live on its own Statement: beginning it
// must not redirect the parent's writes to it.
func TestNestedTx_HooksDontLeakBetweenLevels(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		child := parent.BeginTransaction()
		require.NoError(t, e.rename(t, id, "p", parent))
		assert.Len(t, parent.commitFns, 1)
		assert.Empty(t, child.commitFns)

		require.NoError(t, e.rename(t, id, "c", child))
		assert.Len(t, parent.commitFns, 1)
		assert.Len(t, child.commitFns, 1)

		require.NoError(t, child.Commit())
		assert.Len(t, parent.commitFns, 2, "a committed child hands its hooks up")
		assert.Empty(t, child.commitFns)
		require.NoError(t, parent.Commit())
		assert.Equal(t, "c", e.name(t, id))
	})
}

func TestNestedTx_Grandchild(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		child := parent.BeginTransaction()
		grandchild := child.BeginTransaction()
		require.NoError(t, e.rename(t, id, "after", grandchild))
		require.NoError(t, grandchild.Commit())
		require.NoError(t, child.Commit())
		assert.Equal(t, "before", e.name(t, id))

		require.NoError(t, parent.Commit())
		assert.Equal(t, "after", e.name(t, id))
	})
}

// A child still open when its parent finishes shares the parent's fate,
// hooks included: its writes were committed or rolled back with it.
func TestNestedTx_OpenChildSettledWithParent(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		child := parent.BeginTransaction()
		require.NoError(t, e.rename(t, id, "after", child))
		require.NoError(t, parent.Commit())
		assert.True(t, child.committed)
		assert.Equal(t, "after", e.stored(t, id))
		assert.Equal(t, "after", e.name(t, id), "the open child's invalidation ran with the parent's")
		require.NoError(t, child.Commit(), "already settled")

		parent = e.repo.BeginTransaction()
		child = parent.BeginTransaction()
		ran := false
		child.OnRollback(func() { ran = true })
		require.NoError(t, e.rename(t, id, "lost", child))
		require.NoError(t, parent.Rollback())
		assert.True(t, child.rolledBack)
		assert.True(t, ran)
		assert.Equal(t, "after", e.stored(t, id))
		assert.Equal(t, "after", e.name(t, id))
	})
}

func TestNestedTx_Finish(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")
		run := func(name string, fail error) (err error) {
			parent := e.repo.BeginTransaction()
			defer parent.Finish(&err)
			child := parent.BeginTransaction()
			defer child.Finish(&err)
			if err = e.rename(t, id, name, child); err != nil {
				return err
			}
			return fail
		}

		require.Error(t, run("lost", errors.New("boom")))
		assert.Equal(t, "before", e.stored(t, id))
		assert.Equal(t, "before", e.name(t, id))

		require.NoError(t, run("after", nil))
		assert.Equal(t, "after", e.name(t, id))
	})
}

// With PrepareStmt the savepoint commands go to the underlying
// transaction, unprepared, as GORM's own SavePoint does.
func TestNestedTx_PreparedStatements(t *testing.T) {
	forEachNestedTxDBPrepared(t, true, func(t *testing.T, e nestedTxEnv) {
		a := e.create(t, "a0")
		b := e.create(t, "b0")

		parent := e.repo.BeginTransaction()
		committed := parent.BeginTransaction()
		require.NoError(t, committed.Error())
		require.NoError(t, e.rename(t, a, "a1", committed))
		require.NoError(t, committed.Commit())
		rolledBack := parent.BeginTransaction()
		require.NoError(t, e.rename(t, b, "b1", rolledBack))
		require.NoError(t, rolledBack.Rollback())
		require.NoError(t, parent.Commit())

		assert.Equal(t, "a1", e.name(t, a))
		assert.Equal(t, "b0", e.name(t, b))
		assert.Equal(t, "b0", e.stored(t, b))
	})
}

// A savepoint that can't be created leaves the child failing every call.
func TestNestedTx_SavepointErrors(t *testing.T) {
	forEachNestedTxDB(t, func(t *testing.T, e nestedTxEnv) {
		id := e.create(t, "before")

		parent := e.repo.BeginTransaction()
		require.NoError(t, parent.Commit())
		child := parent.BeginTransaction()
		require.Error(t, child.Error())
		require.Error(t, e.rename(t, id, "after", child))
		assert.Error(t, child.Commit())
		assert.Error(t, child.Rollback())
	})

	t.Run("unsupported driver", func(t *testing.T) {
		dsn := filepath.Join(t.TempDir(), "nosp.db")
		db, err := gorm.Open(noSavepointDialector{sqlite.Open(dsn)}, &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		parent := (&GormRepository[nestedTxWidget]{DB: db}).BeginTransaction()
		defer func() { _ = parent.Rollback() }()
		child := parent.BeginTransaction()
		assert.ErrorIs(t, child.Error(), gorm.ErrUnsupportedDriver)
	})
}

// noSavepointDialector hides the wrapped dialector's savepoint support.
type noSavepointDialector struct{ gorm.Dialector }
