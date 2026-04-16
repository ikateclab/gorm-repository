package plugin

import (
	"context"
	"encoding/json"
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
	return false
}

// queryReplace replaces GORM's default gorm:query callback. On cache hit
// it deserializes directly into Dest. On miss it executes the DB query
// (identically to the stock callback) and then caches the result.
func (p *Plugin) queryReplace(db *gorm.DB) {
	if p.shouldSkipCache(db) {
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

	// Try cache.
	data, hit, err := p.cache.Get(ctx, key)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] Get error for %s: %v", key, err)
		}
		// Fall through to DB.
		p.execAndCache(db, key, label)
		return
	}

	if hit {
		if err := json.Unmarshal(data, db.Statement.Dest); err != nil {
			if p.options.debug {
				log.Printf("[cache] unmarshal error for %s: %v", key, err)
			}
			// Corrupted — run actual query and re-cache.
			p.execAndCache(db, key, label)
			return
		}
		p.options.metrics.Hit(label)
		db.RowsAffected = countRows(db.Statement.Dest)
		return
	}

	// Cache miss.
	p.options.metrics.Miss(label)
	p.execAndCache(db, key, label)
}

// execAndCache executes the already-built query, scans results, and
// populates the cache. This mirrors gorm/callbacks.Query but adds
// cache population.
func (p *Plugin) execAndCache(db *gorm.DB, key, label string) {
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

	if db.Error != nil || p.cache == nil {
		return
	}

	// Marshal and cache the result.
	data, err := json.Marshal(db.Statement.Dest)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] marshal error for %s: %v", key, err)
		}
		return
	}

	tags := p.tagsForStatement(db)
	ttl := p.resolveTTL(db)
	ctx := db.Statement.Context
	txMode := ResolveTxMode(db, p.options.defaultTxMode)

	if hook, hasTx := LookupCommitHook(ctx, db.Get); hasTx && txMode == TxDeferred {
		hook.OnCommit(func(commitCtx context.Context) error {
			return p.cache.Set(commitCtx, key, data, tags, ttl)
		})
	} else {
		if setErr := p.cache.Set(ctx, key, data, tags, ttl); setErr != nil {
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

		tags := p.tagsForWrite(db)
		if len(tags) == 0 {
			return
		}

		ctx := db.Statement.Context
		label := modelLabel(db.Statement)

		// Mark pending writes for dirty-state tracking.
		if pt := lookupPendingTracker(db); pt != nil {
			pt.MarkPending()
		}

		txMode := ResolveTxMode(db, p.options.defaultTxMode)

		if hook, hasTx := LookupCommitHook(ctx, db.Get); hasTx && txMode != TxBypass {
			hook.OnCommit(func(commitCtx context.Context) error {
				p.options.metrics.Invalidation(label, reason)
				return p.cache.Invalidate(commitCtx, tags)
			})
		} else {
			p.options.metrics.Invalidation(label, reason)
			if err := p.cache.Invalidate(ctx, tags); err != nil {
				if p.options.debug {
					log.Printf("[cache] Invalidate error: %v", err)
				}
			}
		}
	}
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
