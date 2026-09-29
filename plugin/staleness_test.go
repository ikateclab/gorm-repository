package plugin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// setupWALDB opens a file-backed SQLite database in WAL mode, so a reader
// outside an open transaction sees the last committed state (as it would
// on Postgres) instead of blocking or reading uncommitted rows. It seeds
// test_users row 1 = "before" and warms the cache with it.
func setupWALDB(t *testing.T, opts ...Option) (*gorm.DB, *memory.Cache, *testRecorder) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "cache.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)

	mc := memory.New()
	rec := &testRecorder{}
	require.NoError(t, db.Use(New(mc, append([]Option{WithDefaultTTL(5 * time.Minute), WithEventRecorder(rec)}, opts...)...)))
	require.NoError(t, db.AutoMigrate(&testUser{}))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "before"}).Error)
	var warm testUser
	require.NoError(t, db.First(&warm, 1).Error)
	require.Equal(t, 1, mc.Len())
	rec.reset()
	return db, mc, rec
}

// beginWithHook starts a real transaction exposing hook the way the
// repository's BeginTransaction does, and returns a per-call session.
func beginWithHook(db *gorm.DB, hook CommitHook) (tx *gorm.DB, session func() *gorm.DB) {
	tx = db.Begin()
	tx.Statement.Settings.Store(CommitHookKey, hook)
	return tx, func() *gorm.DB { return tx.Session(&gorm.Session{Context: context.Background()}) }
}

func (r *testRecorder) hasSkip(reason string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.Kind == EventSkip && e.Reason == reason {
			return true
		}
	}
	return false
}

// A write inside a hooked transaction under the default TxBypass used to
// invalidate before commit: a concurrent reader then re-cached the old
// row, which outlived the commit.
func TestStaleness_TxBypassWriteInTx_FreshAfterCommit(t *testing.T) {
	db, _, _ := setupWALDB(t)

	hook := &fakeHook{}
	tx, session := beginWithHook(db, hook)
	require.NoError(t, session().Model(&testUser{ID: 1}).Update("name", "after").Error)

	var during testUser
	require.NoError(t, db.First(&during, 1).Error)
	assert.Equal(t, "before", during.Name, "outside the tx the write isn't visible yet")

	require.NoError(t, tx.Commit().Error)
	require.Len(t, hook.commits, 1, "the invalidation must be deferred to commit")
	for _, fn := range hook.commits {
		require.NoError(t, fn(context.Background()))
	}

	var after testUser
	require.NoError(t, db.First(&after, 1).Error)
	assert.Equal(t, "after", after.Name)
}

// A plain GORM transaction has no CommitHook: reads inside it must not
// populate the cache, or a rolled-back write would be served afterwards.
func TestStaleness_PlainGormTx_RollbackNotCached(t *testing.T) {
	db, _, rec := setupWALDB(t)

	errRollback := errors.New("rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&testUser{ID: 1}).Update("name", "uncommitted").Error)
		var u testUser
		require.NoError(t, tx.First(&u, 1).Error)
		assert.Equal(t, "uncommitted", u.Name)
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	assert.True(t, rec.hasSkip("tx-without-hook"))

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "before", u.Name)
}

type joinedProfile struct {
	ID     uint `gorm:"primarykey"`
	UserID uint
	Bio    string
}

func (joinedProfile) TableName() string { return "joined_profiles" }

type joinedUser struct {
	ID      uint `gorm:"primarykey"`
	Name    string
	Profile joinedProfile `gorm:"foreignKey:UserID"`
}

func (joinedUser) TableName() string { return "joined_users" }

// Cached entries are tagged with the root table only, so a Joins query
// would miss writes to the joined table — it must bypass the cache.
func TestStaleness_Joins_Bypassed(t *testing.T) {
	db, _, rec := setupWALDB(t)
	require.NoError(t, db.AutoMigrate(&joinedUser{}, &joinedProfile{}))
	require.NoError(t, db.Create(&joinedUser{ID: 1, Name: "u", Profile: joinedProfile{ID: 1, Bio: "old"}}).Error)

	var u1 joinedUser
	require.NoError(t, db.Joins("Profile").First(&u1, 1).Error)
	assert.True(t, rec.hasSkip("joins"))

	require.NoError(t, db.Model(&joinedProfile{ID: 1}).Update("bio", "new").Error)

	var u2 joinedUser
	require.NoError(t, db.Joins("Profile").First(&u2, 1).Error)
	assert.Equal(t, "new", u2.Profile.Bio)
}

func TestStaleness_Subquery_Bypassed(t *testing.T) {
	db, mc, rec := setupWALDB(t)
	before := mc.Len()

	var users []testUser
	sub := db.Model(&testUser{}).Select("id").Where("name = ?", "before")
	require.NoError(t, db.Where("id IN (?)", sub).Find(&users).Error)
	require.Len(t, users, 1)

	assert.True(t, rec.hasSkip("multi-table"))
	assert.Equal(t, before, mc.Len())
}

func TestStaleness_RawFind_Bypassed(t *testing.T) {
	db, mc, rec := setupWALDB(t)
	before := mc.Len()

	var u testUser
	require.NoError(t, db.Raw("SELECT * FROM test_users WHERE id = ?", 1).Find(&u).Error)
	assert.Equal(t, "before", u.Name)

	assert.True(t, rec.hasSkip("raw-sql"))
	assert.Equal(t, before, mc.Len())
}

// SELECT ... FOR UPDATE must reach the database to take its row lock.
// SQLite drops the clause from the SQL, so assert on the skip decision.
func TestStaleness_LockingRead_Bypassed(t *testing.T) {
	db, _, rec := setupWALDB(t, WithDefaultTxMode(TxDeferred))

	_, session := beginWithHook(db, &fakeHook{})
	var u testUser
	require.NoError(t, session().Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, 1).Error)

	assert.True(t, rec.hasSkip("locking"))
	assert.Equal(t, 0, len(rec.kindsOf(EventHit)), "a locking read must never be a cache hit")
}

func (r *testRecorder) kindsOf(kind EventKind) []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, e := range r.events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// db.Exec runs GORM's Raw chain, not Create/Update/Delete.
func TestStaleness_ExecWrite_Invalidates(t *testing.T) {
	db, _, _ := setupWALDB(t)

	require.NoError(t, db.Exec(`UPDATE "test_users" SET name = ? WHERE id = ?`, "after", 1).Error)

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "after", u.Name)
}

func TestStaleness_ExecWriteInHookedTx_DeferredToCommit(t *testing.T) {
	db, mc, _ := setupWALDB(t)

	hook := &fakeHook{}
	tx, session := beginWithHook(db, hook)
	require.NoError(t, session().Exec("UPDATE test_users SET name = ? WHERE id = ?", "after", 1).Error)
	assert.Equal(t, 1, mc.Len(), "invalidation must wait for commit")
	require.Len(t, hook.commits, 1)

	require.NoError(t, tx.Commit().Error)
	for _, fn := range hook.commits {
		require.NoError(t, fn(context.Background()))
	}
	assert.Equal(t, 0, mc.Len())
}

func TestExecWriteTable(t *testing.T) {
	cases := map[string]string{
		`UPDATE users SET name = 'x'`:                 "users",
		`update only "users" set name = 'x'`:          "users",
		`INSERT INTO public.users (id) VALUES (1)`:    "users",
		`insert or replace into users(id) values (1)`: "users",
		"DELETE FROM `users` WHERE id = 1":            "users",
		`TRUNCATE TABLE "public"."users"`:             "users",
		`REPLACE INTO users (id) VALUES (1)`:          "users",
	}
	for sql, want := range cases {
		got, ok := execWriteTable(sql)
		assert.True(t, ok, sql)
		assert.Equal(t, want, got, sql)
	}
	for _, sql := range []string{`SELECT 1`, `CREATE TABLE x (id int)`, `WITH x AS (SELECT 1) UPDATE users SET a = 1`} {
		_, ok := execWriteTable(sql)
		assert.False(t, ok, sql)
	}
}

type secretAccount struct {
	ID           uint `gorm:"primarykey"`
	Email        string
	PasswordHash string `json:"-"`
}

func (secretAccount) TableName() string { return "secret_accounts" }

// The cache must round-trip exactly what gorm.Scan populated, whatever
// the struct's json tags say.
func TestCodec_JSONHiddenFieldSurvivesHit(t *testing.T) {
	db, _, rec := setupWALDB(t)
	require.NoError(t, db.AutoMigrate(&secretAccount{}))
	require.NoError(t, db.Create(&secretAccount{ID: 1, Email: "a@b.c", PasswordHash: "hash"}).Error)

	var miss, hit secretAccount
	require.NoError(t, db.First(&miss, 1).Error)
	require.NoError(t, db.First(&hit, 1).Error)
	require.Len(t, rec.kindsOf(EventHit), 1)
	assert.Equal(t, "hash", hit.PasswordHash)
	assert.Equal(t, miss, hit)
}

func TestCodec_PointerToPointerDestHit(t *testing.T) {
	db, _, rec := setupWALDB(t)

	// Dest type is part of the key, so the first **T read is a miss.
	miss, hit := &testUser{}, &testUser{}
	require.NoError(t, db.First(&miss, 1).Error)
	require.NoError(t, db.First(&hit, 1).Error)
	require.Len(t, rec.kindsOf(EventHit), 1)
	assert.Equal(t, "before", hit.Name)
}

func TestCodec_EmptyResultSliceRoundTrips(t *testing.T) {
	db, _, rec := setupWALDB(t)

	var first, second []testUser
	require.NoError(t, db.Where("name = ?", "nobody").Find(&first).Error)
	require.NoError(t, db.Where("name = ?", "nobody").Find(&second).Error)
	require.Len(t, rec.kindsOf(EventHit), 1)
	assert.Empty(t, second)
}

// A pre-populated Dest would leak its values into the cached payload.
func TestCodec_PrepopulatedDest_Bypassed(t *testing.T) {
	db, _, rec := setupWALDB(t)

	u := testUser{Name: "prefilled"}
	require.NoError(t, db.Select("id").First(&u, 1).Error)
	assert.True(t, rec.hasSkip("dest-not-blank"))
	assert.Empty(t, rec.kindsOf(EventSet))
}

func TestCodec_MapDest_Bypassed(t *testing.T) {
	db, _, rec := setupWALDB(t)

	var row map[string]interface{}
	require.NoError(t, db.Model(&testUser{}).Where("id = ?", 1).Take(&row).Error)
	assert.True(t, rec.hasSkip("map-dest"))
}

// Invalidation must run after GORM commits its implicit write transaction.
// GORM refuses to register a callback whose before/after constraints
// conflict, so a probe pinned between the commit and our callback only
// registers if that order holds.
func TestCallbackOrder_InvalidationAfterImplicitCommit(t *testing.T) {
	db, _, _ := setupCachedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)

	ran := false
	require.NoError(t, db.Callback().Update().After(commitOrRollbackName).Before(updateAfterName).
		Register("probe:between", func(tx *gorm.DB) {
			ran = true
			_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
			assert.False(t, inTx, "the implicit transaction must be closed before invalidation")
		}))

	require.NoError(t, db.Model(&testUser{ID: 1}).Update("name", "b").Error)
	assert.True(t, ran)
}
