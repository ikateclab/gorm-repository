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
