package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type testRecorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *testRecorder) Record(_ context.Context, e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *testRecorder) kinds() []EventKind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]EventKind, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}

func (r *testRecorder) last(kind EventKind) Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Kind == kind {
			return r.events[i]
		}
	}
	return Event{}
}

func (r *testRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

func TestEvents_MissSetHit(t *testing.T) {
	rec := &testRecorder{}
	db, _, _ := setupCachedDB(t, WithEventRecorder(rec))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	rec.reset()

	var u1, u2 testUser
	require.NoError(t, db.Find(&u1, 1).Error)
	require.NoError(t, db.Find(&u2, 1).Error)

	assert.Equal(t, []EventKind{EventMiss, EventSet, EventHit}, rec.kinds())
	miss := rec.last(EventMiss)
	assert.NotEmpty(t, miss.Key)
	assert.Contains(t, miss.SQL, "test_users")
	assert.Equal(t, miss.Key, rec.last(EventSet).Key)
	assert.Equal(t, miss.Key, rec.last(EventHit).Key)
}

func TestEvents_BypassIsSkip(t *testing.T) {
	rec := &testRecorder{}
	db, _, _ := setupCachedDB(t, WithEventRecorder(rec))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	rec.reset()

	var u testUser
	require.NoError(t, Bypass(db).First(&u, 1).Error)

	assert.Equal(t, "bypass", rec.last(EventSkip).Reason)
}

func TestEvents_NotFoundIsStored(t *testing.T) {
	rec := &testRecorder{}
	db, mc, _ := setupCachedDB(t, WithEventRecorder(rec))

	var u testUser
	assert.ErrorIs(t, db.First(&u, 99).Error, gorm.ErrRecordNotFound)

	assert.Equal(t, []EventKind{EventMiss, EventSet}, rec.kinds())
	assert.Equal(t, 1, mc.Len())
}

func TestEvents_InvalidateCarriesReasonAndTags(t *testing.T) {
	rec := &testRecorder{}
	db, _, _ := setupCachedDB(t, WithEventRecorder(rec))

	require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "alice"}).Error)

	inv := rec.last(EventInvalidate)
	assert.Equal(t, "create", inv.Reason)
	assert.NotEmpty(t, inv.Tags)
}

// A backend that fails is reported and never breaks the query: a Get
// error falls through to the database without storing, a Set error
// leaves nothing cached, and an Invalidate error is reported inline, or
// returned and counted as a PostCommitError when deferred to commit.
func TestEvents_CacheErrors(t *testing.T) {
	errDown := errors.New("cache down")

	t.Run("get", func(t *testing.T) {
		db, sc, mc, rec, met := setupScriptedDB(t, WithDebug(true))
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
		sc.getErr = errDown

		var u testUser
		require.NoError(t, db.First(&u, 1).Error)
		assert.Equal(t, "alice", u.Name)
		e := rec.last(EventError)
		assert.Equal(t, "get", e.Reason)
		assert.ErrorIs(t, e.Err, errDown)
		assert.NotEmpty(t, e.Tags)
		assert.Zero(t, sc.sets, "no versions to store under")
		assert.Zero(t, mc.Len())
		assert.Zero(t, met.misses)
	})

	t.Run("set", func(t *testing.T) {
		db, sc, mc, rec, _ := setupScriptedDB(t, WithDebug(true))
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
		sc.setErr = errDown

		var u testUser
		require.NoError(t, db.First(&u, 1).Error)
		assert.Equal(t, "alice", u.Name)
		e := rec.last(EventError)
		assert.Equal(t, "set", e.Reason)
		assert.ErrorIs(t, e.Err, errDown)
		assert.Empty(t, rec.kindsOf(EventSet))
		assert.Zero(t, mc.Len())
	})

	t.Run("invalidate inline", func(t *testing.T) {
		db, sc, _, rec, met := setupScriptedDB(t, WithDebug(true))
		sc.invalidateErr = errDown

		require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
		e := rec.last(EventError)
		assert.Equal(t, "invalidate", e.Reason)
		assert.ErrorIs(t, e.Err, errDown)
		assert.Empty(t, rec.kindsOf(EventInvalidate))
		assert.Zero(t, met.postCommitErrors, "nothing was deferred")
	})

	t.Run("invalidate at commit", func(t *testing.T) {
		db, sc, _, rec, met := setupScriptedDB(t)
		require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
		sc.invalidateErr = errDown

		hook := &fakeHook{}
		require.NoError(t, db.Set(CommitHookKey, CommitHook(hook)).Model(&testUser{ID: 1}).Update("name", "bob").Error)
		require.Len(t, hook.commits, 1)
		assert.ErrorIs(t, hook.commits[0](context.Background()), errDown)
		assert.Equal(t, 1, met.postCommitErrors)
		assert.Equal(t, "invalidate", rec.last(EventError).Reason)
	})
}
