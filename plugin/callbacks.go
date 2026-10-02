package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"reflect"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

// callbackNames used for GORM callback registration.
const (
	queryReplaceName = "gorm:query"
	createAfterName  = "gormrepository:cache:after_create"
	updateAfterName  = "gormrepository:cache:after_update"
	deleteAfterName  = "gormrepository:cache:after_delete"
	// A Set deferred to commit this long after its read is dropped: the
	// backend only remembers invalidations for a while.
	maxDeferredSetAge = time.Minute
	invalidateTimeout = 2 * time.Second
)

// registerCallbacks wires query/create/update/delete callbacks into db.
func (p *Plugin) registerCallbacks(db *gorm.DB) error {
	// Replace the default gorm:query with our caching wrapper.
	if err := db.Callback().Query().Replace(queryReplaceName, p.queryReplace); err != nil {
		return err
	}
	if err := db.Callback().Create().After("gorm:create").Register(createAfterName, p.writeCallback("create")); err != nil {
		return err
	}
	if err := db.Callback().Update().After("gorm:update").Register(updateAfterName, p.writeCallback("update")); err != nil {
		return err
	}
	if err := db.Callback().Delete().After("gorm:delete").Register(deleteAfterName, p.writeCallback("delete")); err != nil {
		return err
	}
	return nil
}

// shouldSkipCache returns true if the cache should not be consulted.
func (p *Plugin) shouldSkipCache(db *gorm.DB) bool {
	if p.cache == nil || db.Statement == nil || db.Error != nil {
		return true
	}
	if IsBypassed(db) {
		return true
	}
	// A locking read must reach the database to take the lock.
	if _, ok := db.Statement.Clauses["FOR"]; ok {
		return true
	}
	if db.Statement.Table != "" {
		if mo, ok := p.LookupModel(db.Statement.Table); ok && mo.Bypass {
			return true
		}
	}
	// Inside a transaction with TxBypass → skip.
	if _, hasTx := LookupCommitHook(db.Statement.Context, db.Get); hasTx {
		txMode := ResolveTxMode(db, p.options.defaultTxMode)
		if txMode == TxBypass {
			return true
		}
	}
	// Dirty-state detection.
	if pt := lookupPendingTracker(db); pt != nil && pt.HasPendingWrites() {
		return true
	}
	// GORM's anonymous many2many join structs copy both FK fields' json
	// tags from the related models' PKs — almost always json:"id" on both,
	// which encoding/json silently drops. Never cache these; a named join
	// type registered via db.SetupJoinTable avoids the bypass entirely.
	if isAnonymousStructDest(db.Statement.Dest) {
		return true
	}
	return false
}

// isAnonymousStructDest reports whether dest is an unnamed struct type
// (e.g. one built via reflect.StructOf), after unwrapping ptr/slice/array.
func isAnonymousStructDest(dest interface{}) bool {
	t := reflect.TypeOf(dest)
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
		t = t.Elem()
	}
	return t != nil && t.Kind() == reflect.Struct && t.Name() == ""
}

// queryReplace replaces GORM's default gorm:query callback. On cache hit
// it deserializes directly into Dest. On miss it executes the DB query
// (identically to the stock callback) and then caches the result.
func (p *Plugin) queryReplace(db *gorm.DB) {
	if p.shouldSkipCache(db) {
		if p.cache != nil && db.Statement != nil && db.Error == nil {
			p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: "bypass"})
		}
		callbacks.Query(db)
		return
	}

	// Let GORM build the SQL (parse model, apply scopes, build clauses).
	callbacks.BuildQuerySQL(db)
	if db.Error != nil {
		return
	}

	schemaVer := p.resolveSchemaVersion(db)
	key := CacheKey(db.Statement, schemaVer)
	ctx := db.Statement.Context
	label := modelLabel(db.Statement)

	// A read's tags come from the query alone. With none, nothing could
	// invalidate its entry: it runs past the cache, without a lookup.
	tags := p.readTags(db, schemaVer)
	if len(tags) == 0 {
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "no-tags"})
		p.execAndCache(db, key, label, nil, 0, false)
		return
	}

	data, hit, seq, err := p.cache.Get(ctx, key, tags)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] Get error for %s: %v", key, err)
		}
		p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "get", Tags: tags, Err: err})
		// Fall through to DB, without caching: there's no seq to store under.
		p.execAndCache(db, key, label, tags, 0, false)
		return
	}

	if hit {
		if err := json.Unmarshal(data, db.Statement.Dest); err != nil {
			if p.options.debug {
				log.Printf("[cache] unmarshal error for %s: %v", key, err)
			}
			p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "unmarshal", Tags: tags, Err: err})
			// Corrupted — run actual query and re-cache.
			p.execAndCache(db, key, label, tags, seq, true)
			return
		}
		p.options.metrics.Hit(label)
		p.emit(ctx, Event{Kind: EventHit, Model: label, Key: key, Tags: tags})
		db.RowsAffected = countRows(db.Statement.Dest)
		if bytes.Equal(data, notFound) {
			db.RowsAffected = 0
			if db.Statement.RaiseErrorOnNotFound {
				_ = db.AddError(gorm.ErrRecordNotFound)
			}
		}
		// GORM's processor logs stmt.SQL unconditionally after this callback
		// returns, whenever it's non-empty — regardless of whether a real
		// query ran. BuildQuerySQL above populated it just to derive the
		// cache key, so clear it here to avoid a misleading "SELECT ..."
		// trace log for a query that never touched the database.
		db.Statement.SQL.Reset()
		db.Statement.Vars = nil
		return
	}

	// Cache miss.
	p.options.metrics.Miss(label)
	p.emit(ctx, Event{Kind: EventMiss, Model: label, Key: key, Tags: tags, SQL: db.Statement.SQL.String()})
	p.execAndCache(db, key, label, tags, seq, true)
}

// notFound is what a First/Take/Last that found nothing stores: the hit
// raises gorm.ErrRecordNotFound again, like the query would.
var notFound = []byte("null")

// execAndCache executes the already-built query, scans results, and —
// when populate is set — caches them under tags and seq. This mirrors
// gorm/callbacks.Query but adds cache population.
func (p *Plugin) execAndCache(db *gorm.DB, key, label string, tags []string, seq int64, populate bool) {
	if db.DryRun || db.Error != nil {
		return
	}

	rows, err := db.Statement.ConnPool.QueryContext(
		db.Statement.Context, db.Statement.SQL.String(), db.Statement.Vars...)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	defer func() {
		_ = db.AddError(rows.Close())
	}()
	gorm.Scan(rows, db, 0)

	if db.Statement.Result != nil {
		db.Statement.Result.RowsAffected = db.RowsAffected
	}

	if p.cache == nil || !populate {
		return
	}
	// A not-found is cached too: the read's tags cover the row that may come.
	data := notFound
	if !errors.Is(db.Error, gorm.ErrRecordNotFound) {
		if db.Error != nil {
			p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: label, Key: key, Reason: "query-error", Tags: tags, Err: db.Error})
			return
		}
		var err error
		if data, err = json.Marshal(db.Statement.Dest); err != nil {
			if p.options.debug {
				log.Printf("[cache] marshal error for %s: %v", key, err)
			}
			p.emit(db.Statement.Context, Event{Kind: EventError, Model: label, Key: key, Reason: "marshal", Err: err})
			return
		}
	}

	ctx := db.Statement.Context
	ttl := p.resolveTTL(db)
	txMode := ResolveTxMode(db, p.options.defaultTxMode)

	if hook, hasTx := LookupCommitHook(ctx, db.Get); hasTx && txMode == TxDeferred {
		readAt := time.Now()
		hook.OnCommit(func(commitCtx context.Context) error {
			if time.Since(readAt) > maxDeferredSetAge {
				return nil
			}
			return p.set(commitCtx, key, label, data, tags, ttl, seq)
		})
	} else {
		if setErr := p.set(ctx, key, label, data, tags, ttl, seq); setErr != nil {
			if p.options.debug {
				log.Printf("[cache] Set error for %s: %v", key, setErr)
			}
		}
	}
}

// writeCallback returns a GORM callback that invalidates cache entries
// after a successful create/update/delete.
func (p *Plugin) writeCallback(reason string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if p.cache == nil || db.Statement == nil || db.Error != nil {
			return
		}

		var tags []string
		if p.options.tagStrategy != nil {
			tags = p.options.tagStrategy.WriteTags(db, p.resolveSchemaVersion(db))
		} else {
			tags = p.tagsForWrite(db)
		}
		if len(tags) == 0 {
			return
		}

		// A write that already reached the database invalidates even if the
		// request that made it is gone.
		ctx := context.WithoutCancel(db.Statement.Context)
		label := modelLabel(db.Statement)

		// Mark pending writes for dirty-state tracking.
		if pt := lookupPendingTracker(db); pt != nil {
			pt.MarkPending()
		}

		txMode := ResolveTxMode(db, p.options.defaultTxMode)

		if hook, hasTx := LookupCommitHook(ctx, db.Get); hasTx && txMode != TxBypass {
			hook.OnCommit(func(commitCtx context.Context) error {
				p.options.metrics.Invalidation(label, reason)
				return p.invalidate(commitCtx, label, reason, tags)
			})
		} else {
			p.options.metrics.Invalidation(label, reason)
			ctx, cancel := context.WithTimeout(ctx, invalidateTimeout)
			defer cancel()
			if err := p.invalidate(ctx, label, reason, tags); err != nil {
				if p.options.debug {
					log.Printf("[cache] Invalidate error: %v", err)
				}
			}
		}
	}
}

// readTags derives a SELECT's tags from the query alone, before it runs.
func (p *Plugin) readTags(db *gorm.DB, schemaVer string) []string {
	if p.options.tagStrategy != nil {
		return p.options.tagStrategy.ReadTags(db, schemaVer)
	}
	return p.tagsForStatement(db)
}

// tagsForStatement derives cache tags for a SELECT query.
func (p *Plugin) tagsForStatement(db *gorm.DB) []string {
	stmtTags := TagsFromStatement(db.Statement, p.options.scopeColumns)
	if mo, ok := p.LookupModel(db.Statement.Table); ok {
		stmtTags = append(stmtTags, mo.ExtraTags...)
	}
	return dedupeSorted(stmtTags)
}

// tagsForWrite derives cache tags for a write operation.
func (p *Plugin) tagsForWrite(db *gorm.DB) []string {
	var tags []string
	if db.Statement.Dest != nil {
		tags = append(tags, TagsFromEntity(db.Statement.Dest)...)
	}
	tags = append(tags, TagsFromStatement(db.Statement, p.options.scopeColumns)...)
	if mo, ok := p.LookupModel(db.Statement.Table); ok {
		tags = append(tags, mo.ExtraTags...)
	}
	return dedupeSorted(tags)
}

// resolveTTL returns the TTL for this query.
func (p *Plugin) resolveTTL(db *gorm.DB) time.Duration {
	if mo, ok := p.LookupModel(db.Statement.Table); ok && mo.TTL > 0 {
		return mo.TTL
	}
	if p.options.defaultTTLFunc != nil {
		return p.options.defaultTTLFunc()
	}
	return p.options.defaultTTL
}

// resolveSchemaVersion returns the schema version for this query.
func (p *Plugin) resolveSchemaVersion(db *gorm.DB) string {
	if v, ok := db.Get(SchemaVersionSetting); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return p.options.schemaVersion
}

// lookupPendingTracker extracts a PendingTracker from the session.
func lookupPendingTracker(db *gorm.DB) PendingTracker {
	if db.Statement == nil {
		return nil
	}
	ctx := db.Statement.Context
	if ctx != nil {
		if v := ctx.Value(ctxPendingTrackerKey); v != nil {
			if pt, ok := v.(PendingTracker); ok {
				return pt
			}
		}
	}
	if v, ok := db.Get(PendingTrackerKey); ok {
		if pt, ok := v.(PendingTracker); ok {
			return pt
		}
	}
	return nil
}

// countRows returns the number of rows in dest for RowsAffected on cache hit.
func countRows(dest interface{}) int64 {
	if dest == nil {
		return 0
	}
	v := reflect.ValueOf(dest)
	for v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
		return int64(v.Len())
	}
	return 1
}
