package plugin

import "gorm.io/gorm"

// Setting keys planted on a *gorm.DB session via db.Set / db.Get to
// influence per-call cache behavior. They are exported so the
// repository-level shim can offer typed wrappers (e.g.
// gormrepository.WithTxMode) without re-declaring the keys.
const (
	// BypassSetting — bool. When true the plugin skips the cache for
	// this call.
	BypassSetting = "cache:bypass"
	// TxModeSetting — TxMode. Overrides the plugin's default tx mode.
	TxModeSetting = "cache:tx_mode"
	// SchemaVersionSetting — string. Overrides the schema version
	// prefix on a per-call basis.
	SchemaVersionSetting = "cache:schema_version"
)

// Bypass returns a session that will skip the cache for the next chained
// call.
func Bypass(db *gorm.DB) *gorm.DB {
	return db.Set(BypassSetting, true)
}

// WithTxMode returns a session scoped to a particular TxMode.
func WithTxMode(db *gorm.DB, mode TxMode) *gorm.DB {
	return db.Set(TxModeSetting, mode)
}

// IsBypassed reports whether the caller has flagged this session to skip
// the cache.
func IsBypassed(db *gorm.DB) bool {
	v, ok := db.Get(BypassSetting)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// ResolveTxMode returns the tx mode for this session, falling back to
// fallback when no per-session override is set.
func ResolveTxMode(db *gorm.DB, fallback TxMode) TxMode {
	v, ok := db.Get(TxModeSetting)
	if !ok {
		return fallback
	}
	if m, ok := v.(TxMode); ok {
		return m
	}
	return fallback
}
