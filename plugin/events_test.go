package plugin

import (
	"context"
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

func TestEvents_NotFoundIsSkip(t *testing.T) {
	rec := &testRecorder{}
	db, mc, _ := setupCachedDB(t, WithEventRecorder(rec))

	var u testUser
	assert.ErrorIs(t, db.First(&u, 99).Error, gorm.ErrRecordNotFound)

	assert.Equal(t, []EventKind{EventMiss, EventSkip}, rec.kinds())
	assert.Equal(t, "query-error", rec.last(EventSkip).Reason)
	assert.Equal(t, 0, mc.Len())
}

func TestEvents_InvalidateCarriesReasonAndTags(t *testing.T) {
	rec := &testRecorder{}
	db, _, _ := setupCachedDB(t, WithEventRecorder(rec))

	require.NoError(t, db.Create(&cachedTestUser{ID: 1, Name: "alice"}).Error)

	inv := rec.last(EventInvalidate)
	assert.Equal(t, "create", inv.Reason)
	assert.NotEmpty(t, inv.Tags)
}
