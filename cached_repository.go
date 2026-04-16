package gormrepository

import (
	"github.com/ikateclab/gorm-repository/plugin"
	"gorm.io/gorm"
)

// NewCachedGormRepository creates a GormRepository[T] with the cache
// plugin installed on the given *gorm.DB. It is a convenience wrapper
// around plugin.New + db.Use + NewGormRepository.
//
// The cache plugin is registered once on db via db.Use; subsequent calls
// with the same db will return an error from GORM if the plugin is
// already registered. To share one plugin across multiple repositories,
// call db.Use(plugin.New(...)) once and then use NewGormRepository[T](db)
// for each type.
//
// Example:
//
//	repo := gormrepository.NewCachedGormRepository[User](db, memcache,
//	    plugin.WithDefaultTTL(5*time.Minute),
//	    plugin.WithScopeColumns("account_id"),
//	)
func NewCachedGormRepository[T any](db *gorm.DB, cache plugin.Cache, opts ...plugin.Option) *GormRepository[T] {
	p := plugin.New(cache, opts...)
	if err := db.Use(p); err != nil {
		// Plugin already registered — this is fine; it means a previous
		// call to NewCachedGormRepository (or db.Use) already installed it.
		// GORM plugins are identified by Name() and duplicate registration
		// returns an error, which we silently ignore.
	}
	return NewGormRepository[T](db)
}
