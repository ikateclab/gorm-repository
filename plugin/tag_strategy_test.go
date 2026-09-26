package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ikateclab/gorm-repository/plugin/memory"
)

// recordingStrategy is a TagStrategy that records every call it receives
// and returns fixed, easily-asserted tags — used to verify the plugin
// dispatches to a custom strategy (and stops calling the built-in
// TagsFromStatement/TagsFromEntity path) when one is configured.
type recordingStrategy struct {
	readCalls  int
	writeCalls int
	readTags   []string
	writeTags  []string
}

func (s *recordingStrategy) ReadTags(db *gorm.DB, schemaVersion string) []string {
	s.readCalls++
	return append([]string(nil), s.readTags...)
}

func (s *recordingStrategy) WriteTags(db *gorm.DB, schemaVersion string) []string {
	s.writeCalls++
	return append([]string(nil), s.writeTags...)
}

func TestWithTagStrategy_OverridesBuiltinDerivation(t *testing.T) {
	strategy := &recordingStrategy{
		readTags:  []string{"custom:read"},
		writeTags: []string{"custom:write"},
	}
	mc := memory.New()
	p := New(mc, WithDefaultTTL(time.Minute), WithTagStrategy(strategy))

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Use(p))
	require.NoError(t, db.AutoMigrate(&testUser{}))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	assert.Equal(t, 1, strategy.writeCalls, "Create should invoke the strategy's WriteTags")

	var u testUser
	require.NoError(t, db.First(&u, 1).Error)
	assert.Equal(t, 1, strategy.readCalls, "a cache miss should invoke the strategy's ReadTags")

	// The custom tag must actually be the one registered in the cache —
	// invalidating it should evict the entry.
	require.NoError(t, mc.Invalidate(t.Context(), []string{"custom:read"}))
	assert.Equal(t, 0, mc.Len(), "invalidating the strategy's own tag should evict the entry it tagged")
}

func TestWithTagStrategy_NilMeansBuiltinDerivation(t *testing.T) {
	mc := memory.New()
	p := New(mc, WithDefaultTTL(time.Minute))

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Use(p))
	require.NoError(t, db.AutoMigrate(&testUser{}))

	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	var u testUser
	require.NoError(t, db.First(&u, 1).Error)

	// Built-in derivation tags by table name — confirms no strategy means
	// the existing behavior is unchanged.
	require.NoError(t, mc.Invalidate(t.Context(), []string{"table:test_users"}))
	assert.Equal(t, 0, mc.Len())
}
