package plugin

import "gorm.io/gorm"

// idHintSetting is the db.Set/db.Get key repository methods that already
// know the row's id as a parameter (DeleteById, UpdateByIdWithMap) use to
// pass it straight through to a TagStrategy, instead of leaving the
// strategy to reverse-engineer it from the compiled WHERE clause.
const idHintSetting = "gormrepository:id_hint"

// WithIDHint returns a session carrying id, so a TagStrategy's WriteTags
// can read it back via IDHint instead of parsing SQL. Repository methods
// that build a query from a known id but don't leave it on Dest/Model
// (e.g. db.Delete(new(T), "id = ?", id) — Dest is a blank T) should call
// this before executing.
func WithIDHint(db *gorm.DB, id string) *gorm.DB {
	return db.Set(idHintSetting, id)
}

// IDHint returns the id passed to WithIDHint for this session, if any.
func IDHint(db *gorm.DB) (string, bool) {
	v, ok := db.Get(idHintSetting)
	if !ok {
		return "", false
	}
	id, ok := v.(string)
	return id, ok && id != ""
}
