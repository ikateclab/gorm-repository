package plugin

import (
	"context"
	"errors"
	"fmt"
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
		// Postgres ONLY; MySQL priority/IGNORE modifiers; SQLite OR <conflict>.
		`DELETE FROM ONLY users WHERE id = 1`:                 "users",
		`delete low_priority quick ignore from users`:         "users",
		`INSERT IGNORE INTO users (id) VALUES (1)`:            "users",
		`INSERT LOW_PRIORITY IGNORE INTO users (id) VALUES 1`: "users",
		`REPLACE DELAYED INTO users (id) VALUES (1)`:          "users",
		"UPDATE LOW_PRIORITY IGNORE `users` SET a = 1":        "users",
		`UPDATE OR IGNORE users SET a = 1`:                    "users",
		// Leading comments (query tags) are skipped.
		`/* app=api */ UPDATE users SET a = 1`:                 "users",
		"-- tagged\nDELETE FROM users WHERE id = 1":            "users",
		"/* a */ /* b\n */ -- c\n  INSERT INTO users VALUES 1": "users",
	}
	for sql, want := range cases {
		got, ok := execWriteTable(sql)
		assert.True(t, ok, sql)
		assert.Equal(t, want, got, sql)
	}
	for _, sql := range []string{`SELECT 1`, `CREATE TABLE x (id int)`, `WITH x AS (SELECT 1) UPDATE users SET a = 1`, `/* UPDATE users */ SELECT 1`} {
		_, ok := execWriteTable(sql)
		assert.False(t, ok, sql)
	}

	// Writes whose table can't be found are still recognised as writes.
	for _, sql := range []string{`WITH x AS (SELECT 1) UPDATE users SET a = 1`, `/* c */ MERGE INTO users USING x ON true`} {
		assert.True(t, execLooksLikeWriteRe.MatchString(stripLeadingComments(sql)), sql)
	}
	for _, sql := range []string{`WITH x AS (SELECT 1) SELECT * FROM x`, `SELECT 1`, `SAVEPOINT sp1`} {
		assert.False(t, execLooksLikeWriteRe.MatchString(stripLeadingComments(sql)), sql)
	}
}

// A comment-prefixed db.Exec used to go unrecognised, so it never
// invalidated.
func TestStaleness_ExecWithLeadingComment_Invalidates(t *testing.T) {
	db, _, _ := setupWALDB(t)

	require.NoError(t, db.Exec("/* app=api */ UPDATE test_users SET name = ? WHERE id = ?", "after", 1).Error)

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "after", u.Name)
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

// GORM tags an aliased Table("test_users u") read with the alias, so a
// write to test_users never invalidated it; a comma join or subquery table
// reads a relation the entry isn't tagged with at all. All bypass now.
func TestStaleness_TableExpressions_Bypassed(t *testing.T) {
	queries := map[string]func(*gorm.DB) *gorm.DB{
		"alias":    func(db *gorm.DB) *gorm.DB { return db.Table("test_users u").Where("u.id = ?", 1) },
		"alias AS": func(db *gorm.DB) *gorm.DB { return db.Table("test_users AS u").Where("u.id = ?", 1) },
		"comma join": func(db *gorm.DB) *gorm.DB {
			return db.Table("test_users, cached_test_users").Select("test_users.*").Where("cached_test_users.name = ?", "x")
		},
		"subquery": func(db *gorm.DB) *gorm.DB {
			return db.Table("(SELECT * FROM test_users) AS t").Where("t.id = ?", 1)
		},
		"args": func(db *gorm.DB) *gorm.DB { return db.Table("(SELECT * FROM test_users WHERE id = ?) AS t", 1) },
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			db, mc, rec := setupWALDB(t)
			require.NoError(t, db.AutoMigrate(&cachedTestUser{}))
			require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "x"}).Error)
			before := mc.Len()

			var first []testUser
			require.NoError(t, query(db).Find(&first).Error)
			require.Len(t, first, 1)
			assert.True(t, rec.hasSkip("table-expr"))
			assert.Equal(t, before, mc.Len())

			require.NoError(t, db.Model(&testUser{ID: 1}).Update("name", "after").Error)
			var second []testUser
			require.NoError(t, query(db).Find(&second).Error)
			require.Len(t, second, 1)
			assert.Equal(t, "after", second[0].Name)
		})
	}
}

// Table() naming just the table, quoted or schema-qualified, still caches:
// GORM sets TableExpr for it too.
func TestStaleness_PlainTableName_StillCached(t *testing.T) {
	for _, table := range []string{"test_users", "main.test_users"} {
		t.Run(table, func(t *testing.T) {
			db, _, rec := setupWALDB(t)
			for i := 0; i < 2; i++ {
				var users []testUser
				require.NoError(t, db.Table(table).Where("id = ?", 1).Find(&users).Error)
				require.Len(t, users, 1)
			}
			assert.Len(t, rec.kindsOf(EventHit), 1)
			assert.False(t, rec.hasSkip("table-expr"))
		})
	}
}

// db.Raw("UPDATE ... RETURNING").Scan/Find writes through the Row and
// Query chains, which used to have no invalidation hook.
func TestStaleness_RawReturningWrite_Invalidates(t *testing.T) {
	ways := map[string]func(db *gorm.DB, name string) error{
		"Scan (Row chain)": func(db *gorm.DB, name string) error {
			var out []testUser
			return db.Raw("UPDATE test_users SET name = ? WHERE id = ? RETURNING *", name, 1).Scan(&out).Error
		},
		"Find (Query chain)": func(db *gorm.DB, name string) error {
			var out []testUser
			return db.Raw("UPDATE test_users SET name = ? WHERE id = ? RETURNING *", name, 1).Find(&out).Error
		},
		"Row (Row chain)": func(db *gorm.DB, name string) error {
			var id int
			return db.Raw("UPDATE test_users SET name = ? WHERE id = ? RETURNING id", name, 1).Row().Scan(&id)
		},
	}
	for name, write := range ways {
		t.Run(name, func(t *testing.T) {
			db, mc, _ := setupWALDB(t)
			require.NoError(t, write(db, "after"))
			assert.Equal(t, 0, mc.Len())

			var u testUser
			require.NoError(t, db.First(&u, 1).Error)
			assert.Equal(t, "after", u.Name)
		})
	}

	// Inside a hooked transaction the read skips the cache (tx-bypass), and
	// its write is still found and deferred to commit.
	t.Run("in hooked tx", func(t *testing.T) {
		db, mc, _ := setupWALDB(t)
		hook := &fakeHook{}
		tx, session := beginWithHook(db, hook)
		var out []testUser
		require.NoError(t, session().Raw("UPDATE test_users SET name = ? WHERE id = ? RETURNING *", "after", 1).Find(&out).Error)
		require.Len(t, out, 1)
		require.Len(t, hook.commits, 1)
		assert.Equal(t, 1, mc.Len(), "invalidation waits for commit")

		require.NoError(t, tx.Commit().Error)
		require.NoError(t, hook.commits[0](context.Background()))
		var u testUser
		require.NoError(t, db.First(&u, 1).Error)
		assert.Equal(t, "after", u.Name)
	})
}

type afterUpdateErrUser struct {
	ID   uint `gorm:"primarykey"`
	Name string
}

func (afterUpdateErrUser) TableName() string { return "after_update_err_users" }

func (u *afterUpdateErrUser) AfterUpdate(*gorm.DB) error {
	if u.Name == "boom" {
		return errors.New("after update failed")
	}
	return nil
}

// Without GORM's implicit transaction, a write is committed before a later
// callback fails. Write callbacks used to skip on any error, leaving that
// committed write cached stale.
func TestStaleness_WriteThenCallbackError_StillInvalidates(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "cache.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{SkipDefaultTransaction: true, Logger: logger.Discard})
	require.NoError(t, err)
	mc := memory.New()
	require.NoError(t, db.Use(New(mc)))
	require.NoError(t, db.AutoMigrate(&afterUpdateErrUser{}))
	require.NoError(t, db.Create(&afterUpdateErrUser{ID: 1, Name: "before"}).Error)
	var warm afterUpdateErrUser
	require.NoError(t, db.First(&warm, 1).Error)

	require.Error(t, db.Model(&afterUpdateErrUser{ID: 1}).Updates(afterUpdateErrUser{Name: "boom"}).Error)

	var stored, served afterUpdateErrUser
	require.NoError(t, Bypass(db).First(&stored, 1).Error)
	require.Equal(t, "boom", stored.Name, "the write was committed")
	require.NoError(t, db.First(&served, 1).Error)
	assert.Equal(t, stored.Name, served.Name)
}

// A read whose lookup came before a write's invalidation must not store
// what it read: the memory backend refuses the Set. Exercised end to end,
// with the write landing between the read's query and its Set.
func TestStaleness_ReadOverlappingWrite_NotStored(t *testing.T) {
	newDB := func(t *testing.T) (*gorm.DB, *scriptedCache, *memory.Cache, *testRecorder) {
		dsn := filepath.Join(t.TempDir(), "cache.db") + "?_journal_mode=WAL&_busy_timeout=5000"
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Discard})
		require.NoError(t, err)
		mc := memory.New()
		sc := &scriptedCache{Cache: mc}
		rec := &testRecorder{}
		require.NoError(t, db.Use(New(sc, WithEventRecorder(rec))))
		require.NoError(t, db.AutoMigrate(&testUser{}))
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "before"}).Error)
		return db, sc, mc, rec
	}
	writeOnce := func(db *gorm.DB, sc *scriptedCache) {
		sc.beforeSet = func(context.Context, []string) {
			sc.beforeSet = nil
			if err := db.Model(&testUser{ID: 1}).Update("name", "after").Error; err != nil {
				panic(err)
			}
		}
	}

	t.Run("miss", func(t *testing.T) {
		db, sc, mc, _ := newDB(t)
		writeOnce(db, sc)

		var u testUser
		require.NoError(t, db.First(&u, 1).Error)
		assert.Equal(t, "before", u.Name, "the read ran before the write")
		assert.Equal(t, 0, mc.Len(), "and must not be stored after it")

		var fresh testUser
		require.NoError(t, db.First(&fresh, 1).Error)
		assert.Equal(t, "after", fresh.Name)
	})

	// After a decode error the re-read stores under the versions the hit's
	// lookup returned, so it is refused the same way.
	t.Run("decode error", func(t *testing.T) {
		db, sc, mc, rec := newDB(t)
		var warm testUser
		require.NoError(t, db.First(&warm, 1).Error)
		miss := rec.last(EventMiss)
		// Corrupt the entry without bumping any version.
		ctx := context.Background()
		_, _, versions, err := mc.Get(ctx, miss.Key, miss.Tags)
		require.NoError(t, err)
		require.NoError(t, mc.Set(ctx, miss.Key, []byte("garbage"), miss.Tags, 0, versions))
		writeOnce(db, sc)

		var u testUser
		require.NoError(t, db.First(&u, 1).Error)
		assert.Equal(t, "decode", rec.last(EventError).Reason)
		assert.Equal(t, sc.lastGetVersions, sc.lastSetVersions, "the re-read stores under the lookup's versions")
		assert.Equal(t, 0, mc.Len())

		var fresh testUser
		require.NoError(t, db.First(&fresh, 1).Error)
		assert.Equal(t, "after", fresh.Name)
	})
}

// In every TxMode, a write inside a transaction that exposes a CommitHook
// invalidates on commit, never before it.
func TestStaleness_HookedTxWrites_DeferredInEveryTxMode(t *testing.T) {
	writes := map[string]func(*gorm.DB) error{
		"create": func(tx *gorm.DB) error { return tx.Create(&testUser{ID: 2, Name: "new"}).Error },
		"update": func(tx *gorm.DB) error { return tx.Model(&testUser{ID: 1}).Update("name", "after").Error },
		"delete": func(tx *gorm.DB) error { return tx.Delete(&testUser{}, 1).Error },
		"exec": func(tx *gorm.DB) error {
			return tx.Exec("UPDATE test_users SET name = ? WHERE id = ?", "after", 1).Error
		},
	}
	for _, mode := range []TxMode{TxBypass, TxDeferred, TxFull} {
		for name, write := range writes {
			t.Run(fmt.Sprintf("mode %d/%s", mode, name), func(t *testing.T) {
				db, mc, _ := setupWALDB(t, WithDefaultTxMode(mode))
				pt := &fakePendingTracker{}
				hook := &fakeHook{}
				tx, session := beginWithHook(db, hook)
				tx.Statement.Settings.Store(PendingTrackerKey, PendingTracker(pt))

				require.NoError(t, write(session()))
				require.Len(t, hook.commits, 1)
				assert.True(t, pt.pending)
				assert.Equal(t, 1, mc.Len(), "nothing is invalidated before commit")

				require.NoError(t, tx.Commit().Error)
				require.NoError(t, hook.commits[0](context.Background()))
				assert.Equal(t, 0, mc.Len())
			})
		}
	}
}

// Queries reading another relation through the WHERE clause or the table
// expression, and locking reads, all skip the cache.
func TestStaleness_OtherRelationsAndLocks_Bypassed(t *testing.T) {
	cases := []struct {
		name, reason string
		query        func(*gorm.DB) *gorm.DB
	}{
		{"exists", "multi-table", func(db *gorm.DB) *gorm.DB {
			return db.Where("EXISTS (SELECT 1 FROM cached_test_users c WHERE c.id = test_users.id)")
		}},
		{"union", "multi-table", func(db *gorm.DB) *gorm.DB {
			return db.Where("id IN (SELECT id FROM test_users UNION SELECT id FROM cached_test_users)")
		}},
		{"join in table", "table-expr", func(db *gorm.DB) *gorm.DB {
			return db.Table("test_users JOIN cached_test_users ON cached_test_users.id = test_users.id").Select("test_users.*")
		}},
		{"lock share", "locking", func(db *gorm.DB) *gorm.DB {
			return db.Clauses(clause.Locking{Strength: "SHARE"}).Where("id = ?", 1)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mc, rec := setupWALDB(t)
			require.NoError(t, db.AutoMigrate(&cachedTestUser{}))
			require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "x"}).Error)
			before := mc.Len()

			var users []testUser
			require.NoError(t, tc.query(db).Find(&users).Error)
			require.Len(t, users, 1)
			assert.True(t, rec.hasSkip(tc.reason), "want skip %q", tc.reason)
			assert.Equal(t, before, mc.Len())
		})
	}
}

// The create and delete invalidations, like update's, run after GORM
// commits its implicit write transaction (see
// TestCallbackOrder_InvalidationAfterImplicitCommit).
func TestCallbackOrder_CreateAndDeleteInvalidateAfterImplicitCommit(t *testing.T) {
	db, _, _ := setupCachedDB(t)

	ran := map[string]bool{}
	probe := func(name string) func(*gorm.DB) {
		return func(tx *gorm.DB) {
			ran[name] = true
			_, inTx := tx.Statement.ConnPool.(gorm.TxCommitter)
			assert.False(t, inTx, "the implicit transaction must be closed before invalidation")
		}
	}
	require.NoError(t, db.Callback().Create().After(commitOrRollbackName).Before(createAfterName).Register("probe:create", probe("create")))
	require.NoError(t, db.Callback().Delete().After(commitOrRollbackName).Before(deleteAfterName).Register("probe:delete", probe("delete")))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "a"}).Error)
	require.NoError(t, db.Delete(&testUser{}, 1).Error)
	assert.True(t, ran["create"])
	assert.True(t, ran["delete"])
}

// A CommitHook or PendingTracker carried by the context works like one
// stored on the session.
func TestStaleness_HookAndTrackerInContext(t *testing.T) {
	db, mc, rec := setupWALDB(t, WithDefaultTxMode(TxDeferred))

	hook := &fakeHook{}
	pt := &fakePendingTracker{}
	tx := db.Begin()
	ctx := PendingTrackerContext(CommitHookContext(context.Background(), hook), pt)
	session := func() *gorm.DB { return tx.WithContext(ctx) }

	var cached testUser
	require.NoError(t, session().First(&cached, 1).Error)
	assert.Len(t, rec.kindsOf(EventHit), 1, "the hook makes this a hooked transaction, so TxDeferred may serve it")

	require.NoError(t, session().Model(&testUser{ID: 1}).Update("name", "after").Error)
	require.Len(t, hook.commits, 1)
	assert.True(t, pt.pending)
	assert.Equal(t, 1, mc.Len())

	var dirty testUser
	require.NoError(t, session().First(&dirty, 1).Error)
	assert.Equal(t, "after", dirty.Name)
	assert.True(t, rec.hasSkip("pending-writes"))

	require.NoError(t, tx.Commit().Error)
	require.NoError(t, hook.commits[0](context.Background()))
	assert.Equal(t, 0, mc.Len())
}

// A plain GORM transaction has no hook: its reads skip the cache, and its
// writes invalidate inline. Once committed, reads are fresh.
func TestStaleness_PlainGormTx_CommitFresh(t *testing.T) {
	db, _, rec := setupWALDB(t)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var u testUser
		if err := tx.First(&u, 1).Error; err != nil {
			return err
		}
		return tx.Model(&testUser{ID: 1}).Update("name", "after").Error
	}))
	assert.True(t, rec.hasSkip("tx-without-hook"))

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, "after", u.Name)
}

func TestExecWriteTables_TruncateList(t *testing.T) {
	cases := map[string][]string{
		`TRUNCATE a, b`: {"a", "b"},
		`TRUNCATE TABLE "public"."a", b RESTART IDENTITY`:  {"a", "b"},
		`truncate only a, only b * cascade;`:               {"a", "b"},
		"/* tag */ TRUNCATE TABLE `a`":                     {"a"},
		`UPDATE users SET a = 1`:                           {"users"},
		`DELETE FROM users WHERE id IN (SELECT id FROM b)`: {"users"},
	}
	for sql, want := range cases {
		assert.Equal(t, want, execWriteTables(sql), sql)
	}
	assert.Empty(t, execWriteTables(`SELECT 1`))
}

// TRUNCATE can list several tables; every one must be invalidated, not
// just the first. SQLite has no TRUNCATE, so the statement fails, but raw
// write invalidation runs even when the statement reports an error.
func TestStaleness_TruncateList_InvalidatesEveryTable(t *testing.T) {
	db, _, rec := setupWALDB(t)

	_ = db.Exec(`TRUNCATE test_users, secret_accounts`).Error

	var tags []string
	for _, e := range rec.kindsOf(EventInvalidate) {
		tags = append(tags, e.Tags...)
	}
	assert.Contains(t, tags, "table:test_users")
	assert.Contains(t, tags, "table:secret_accounts")
}
