package plugin

import "gorm.io/gorm"

// TagStrategy lets a caller replace the plugin's built-in tag derivation
// (table name + WHERE-clause scope columns) with a custom one — e.g. to
// keep tags wire-compatible with another service sharing the same Redis.
// ReadTags computes a SELECT's tags from the query alone, before it runs — a
// read it gives none to skips the cache; WriteTags computes tags to
// invalidate after a create/update/delete.
type TagStrategy interface {
	ReadTags(db *gorm.DB, schemaVersion string) []string
	WriteTags(db *gorm.DB, schemaVersion string) []string
}

// WithTagStrategy overrides the plugin's tag derivation entirely. When
// set, neither WithScopeColumns nor the CacheTaggable/QueryTagger
// extension points are consulted — the strategy owns tag derivation
// completely.
func WithTagStrategy(s TagStrategy) Option {
	return func(o *options) { o.tagStrategy = s }
}
