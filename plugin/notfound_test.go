package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCallback_NotFound_IsCachedUntilTheRowIsCreated(t *testing.T) {
	db, _, met := setupCachedDB(t)

	var u testUser
	require.ErrorIs(t, db.First(&u, 7).Error, gorm.ErrRecordNotFound)
	res := db.First(&u, 7)
	require.ErrorIs(t, res.Error, gorm.ErrRecordNotFound, "a cached not-found raises like the query")
	assert.Zero(t, res.RowsAffected)
	assert.Equal(t, 1, met.misses)
	assert.Equal(t, 1, met.hits)

	require.NoError(t, db.Create(&testUser{ID: 7, Name: "new"}).Error)
	require.NoError(t, db.First(&u, 7).Error)
	assert.Equal(t, "new", u.Name)
}

// A Find into a struct that matched nothing used to cache the blank
// struct with RowsAffected guessed as 1. A Take with the same SQL shares
// the key, and was then served that blank struct instead of raising.
func TestCallback_FindIntoStructMatchingNothing_TakeStillRaises(t *testing.T) {
	db, _, mc, rec, met := setupScriptedDB(t)

	var a testUser
	miss := db.Where("id = ?", 42).Limit(1).Find(&a)
	require.NoError(t, miss.Error)
	require.Zero(t, miss.RowsAffected)
	stored, ok, _, err := mc.Get(t.Context(), rec.last(EventMiss).Key, nil)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, notFound, stored, "stored as a not-found, not a blank struct")

	var b testUser
	hit := db.Where("id = ?", 42).Limit(1).Find(&b)
	require.NoError(t, hit.Error, "Find never raises not-found, hit or miss")
	assert.Zero(t, hit.RowsAffected)
	assert.Equal(t, 1, met.hits)

	var c testUser
	take := db.Where("id = ?", 42).Take(&c)
	require.ErrorIs(t, take.Error, gorm.ErrRecordNotFound)
	assert.Zero(t, take.RowsAffected)
	assert.Equal(t, 2, met.hits, "Take shares the Find's key")

	// A scalar Dest stores its (zero) value with the row count, and the
	// hit raises from that count, as gorm.Scan does.
	var name string
	require.NoError(t, db.Model(&testUser{}).Select("name").Where("id = ?", 42).Limit(1).Find(&name).Error)
	take = db.Model(&testUser{}).Select("name").Where("id = ?", 42).Take(&name)
	require.ErrorIs(t, take.Error, gorm.ErrRecordNotFound)
	assert.Equal(t, 3, met.hits)
}

func TestCallback_TakeAndLastNotFound_ReraisedOnHit(t *testing.T) {
	db, _, met := setupCachedDB(t)

	for _, find := range []func(*gorm.DB, *testUser) *gorm.DB{
		func(db *gorm.DB, u *testUser) *gorm.DB { return db.Take(u, 99) },
		func(db *gorm.DB, u *testUser) *gorm.DB { return db.Last(u, 99) },
	} {
		hitsBefore := met.hits
		var miss, hit testUser
		require.ErrorIs(t, find(db, &miss).Error, gorm.ErrRecordNotFound)
		res := find(db, &hit)
		require.ErrorIs(t, res.Error, gorm.ErrRecordNotFound)
		assert.Zero(t, res.RowsAffected)
		assert.Equal(t, hitsBefore+1, met.hits)
	}
}

// A hit reports the miss's RowsAffected, and raises only where the query
// would: a Find matching nothing never does.
func TestCallback_LimitFindNotFound_RaisesNothing(t *testing.T) {
	db, _, met := setupCachedDB(t)

	for i := 0; i < 2; i++ {
		var u testUser
		res := db.Limit(1).Find(&u, 99)
		require.NoError(t, res.Error)
		assert.Zero(t, res.RowsAffected)
		assert.Zero(t, u.ID)
	}
	assert.Equal(t, 1, met.hits)
}
