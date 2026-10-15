package rediscache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ikateclab/gorm-repository/plugin"
)

type e2eUser struct {
	ID           uint `gorm:"primarykey"`
	Name         string
	PasswordHash string `json:"-"`
}

type hitCounter struct{ hits, misses int }

func (m *hitCounter) Hit(string)                  { m.hits++ }
func (m *hitCounter) Miss(string)                 { m.misses++ }
func (m *hitCounter) Invalidation(string, string) {}
func (m *hitCounter) PostCommitError()            {}

// The plugin's gob payloads must survive this backend's envelope, and the
// whole read → hit → write → fresh-read cycle must work against Redis.
func TestPlugin_EndToEndThroughRedis(t *testing.T) {
	met := &hitCounter{}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	require.NoError(t, db.Use(plugin.New(New(setupRedis(t)), plugin.WithDefaultTTL(time.Minute), plugin.WithMetrics(met))))
	require.NoError(t, db.AutoMigrate(&e2eUser{}))
	require.NoError(t, db.Create(&e2eUser{ID: 1, Name: "before", PasswordHash: "hash"}).Error)

	var miss, hit e2eUser
	require.NoError(t, db.First(&miss, 1).Error)
	require.NoError(t, db.First(&hit, 1).Error)
	require.Equal(t, 1, met.hits)
	require.Equal(t, miss, hit)
	require.Equal(t, "hash", hit.PasswordHash)

	require.NoError(t, db.Model(&e2eUser{ID: 1}).Update("name", "after").Error)
	var fresh e2eUser
	require.NoError(t, db.First(&fresh, 1).Error)
	require.Equal(t, "after", fresh.Name)

	// A cached not-found raises like the query would.
	var none e2eUser
	require.ErrorIs(t, db.First(&none, 99).Error, gorm.ErrRecordNotFound)
	hitsBefore := met.hits
	require.ErrorIs(t, db.First(&none, 99).Error, gorm.ErrRecordNotFound)
	require.Equal(t, hitsBefore+1, met.hits)
}
