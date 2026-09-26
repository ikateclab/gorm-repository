package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func TestBypass(t *testing.T) {
	db := newTestDB(t)
	assert.False(t, IsBypassed(db))

	bypassed := Bypass(db)
	assert.True(t, IsBypassed(bypassed))

	// Original db is unchanged (Set returns a new session).
	assert.False(t, IsBypassed(db))
}

func TestResolveTxMode(t *testing.T) {
	db := newTestDB(t)
	assert.Equal(t, TxBypass, ResolveTxMode(db, TxBypass))
	assert.Equal(t, TxDeferred, ResolveTxMode(db, TxDeferred))

	scoped := WithTxMode(db, TxFull)
	assert.Equal(t, TxFull, ResolveTxMode(scoped, TxBypass))

	// Wrong type stored under the key falls back.
	bad := db.Set(TxModeSetting, "not-a-tx-mode")
	assert.Equal(t, TxBypass, ResolveTxMode(bad, TxBypass))
}
