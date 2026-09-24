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

// preloadParent/preloadChild exercise a Preload relationship — GORM runs
// the child query separately from the parent's, through this same plugin
// recursively, so it's cached and invalidated on its own.
type preloadParent struct {
	ID       string `gorm:"primarykey"`
	Children []preloadChild `gorm:"foreignKey:ParentID"`
}

func (preloadParent) TableName() string { return "preload_parents" }

type preloadChild struct {
	ID       string `gorm:"primarykey"`
	ParentID string
}

func (preloadChild) TableName() string { return "preload_children" }

// TestPreload_CachedIndependentlyOfParent is the load-bearing test behind
// a real simplification: a TagStrategy does NOT need to walk Preloads and
// tag the parent with each child's tags (unlike the Node backend's
// makeIncludeTags, which must — it caches the whole composed object graph
// as one blob per call). Here, each Preload runs as its own query through
// this same plugin, so it gets its own cache entry with its own tags,
// entirely independent of whatever happened to the parent's entry. A
// write that only tags the child must still bust a Preload'd read of the
// parent, with zero parent-side bookkeeping.
func TestPreload_CachedIndependentlyOfParent(t *testing.T) {
	mc := memory.New()
	p := New(mc, WithDefaultTTL(time.Minute))
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Use(p))
	require.NoError(t, db.AutoMigrate(&preloadParent{}, &preloadChild{}))

	require.NoError(t, db.Create(&preloadParent{ID: "p1"}).Error)
	require.NoError(t, db.Create(&preloadChild{ID: "c1", ParentID: "p1"}).Error)

	var first preloadParent
	require.NoError(t, db.Preload("Children").First(&first, "id = ?", "p1").Error)
	require.Len(t, first.Children, 1)
	entriesAfterFirstRead := mc.Len() // one for the parent row, one for the Children sub-query

	// A write that only touches the child (no tag on the parent at all)
	// must still make the next Preload'd read see fresh child data —
	// without WriteTags/ReadTags ever walking Preloads.
	require.NoError(t, db.Delete(&preloadChild{}, "id = ?", "c1").Error)

	var second preloadParent
	require.NoError(t, db.Preload("Children").First(&second, "id = ?", "p1").Error)
	assert.Empty(t, second.Children, "the child's own invalidation must reach a Preload'd read of the parent")

	// And the reverse: entries are genuinely independent, not one entry
	// silently reused for both — deleting the child didn't wipe out the
	// parent's own cache entry too (both were re-populated after the
	// child's own tag was busted).
	assert.Equal(t, entriesAfterFirstRead, mc.Len(), "same number of independent entries after the second read")
}
