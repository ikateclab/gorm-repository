package plugin

import (
	"context"
	"log"
	"reflect"
	"regexp"
	"strings"
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
	rawAfterName     = "gormrepository:cache:after_raw"

	// commitOrRollbackName is GORM's callback that commits the implicit
	// per-write transaction (when SkipDefaultTransaction is off).
	commitOrRollbackName = "gorm:commit_or_rollback_transaction"
)

// registerCallbacks wires query/create/update/delete/raw callbacks into db.
func (p *Plugin) registerCallbacks(db *gorm.DB) error {
	// Replace the default gorm:query with our caching wrapper.
	if err := db.Callback().Query().Replace(queryReplaceName, p.queryReplace); err != nil {
		return err
	}
	// Write callbacks must run after GORM commits its implicit per-write
	// transaction (which itself follows gorm:create/update/delete):
	// invalidating earlier lets a concurrent reader miss, read the
	// still-committed old row and re-cache it.
	if err := db.Callback().Create().After(commitOrRollbackName).Register(createAfterName, p.writeCallback("create")); err != nil {
		return err
	}
	if err := db.Callback().Update().After(commitOrRollbackName).Register(updateAfterName, p.writeCallback("update")); err != nil {
		return err
	}
	if err := db.Callback().Delete().After(commitOrRollbackName).Register(deleteAfterName, p.writeCallback("delete")); err != nil {
		return err
	}
	// db.Exec runs the Raw chain, not Create/Update/Delete, so raw writes
	// need their own invalidation hook.
	if err := db.Callback().Raw().After("gorm:raw").Register(rawAfterName, p.execWriteCallback); err != nil {
		return err
	}
	return nil
}

// txState reports whether db runs inside a transaction, and the CommitHook
// that transaction exposes, if any. A plain GORM transaction (db.Begin,
// db.Transaction) is detected through its ConnPool but has no hook.
func txState(db *gorm.DB) (CommitHook, bool) {
	if hook, ok := LookupCommitHook(db.Statement.Context, db.Get); ok {
		return hook, true
	}
	_, inTx := db.Statement.ConnPool.(gorm.TxCommitter)
	return nil, inTx
}

// skipReason returns why the cache must not be consulted for this query,
// or "" when it may be. It only looks at state available before the SQL
// is built; see uncacheableQuery for the rest.
func (p *Plugin) skipReason(db *gorm.DB) string {
	if IsBypassed(db) {
		return "bypass"
	}
	if db.Statement.Table != "" {
		if mo, ok := p.LookupModel(db.Statement.Table); ok && mo.Bypass {
			return "model-bypass"
		}
	}
	if hook, inTx := txState(db); inTx {
		// Without a hook nothing can be deferred to commit, so a read here
		// could cache rows that are uncommitted or later rolled back.
		if hook == nil {
			return "tx-without-hook"
		}
		if ResolveTxMode(db, p.options.defaultTxMode) == TxBypass {
			return "tx-bypass"
		}
	}
	// Dirty-state detection.
	if pt := lookupPendingTracker(db); pt != nil && pt.HasPendingWrites() {
		return "pending-writes"
	}
	// GORM's anonymous many2many join structs (built via reflect.StructOf)
	// have no stable type name to key or decode against. Never cache
	// these; a named join type registered via db.SetupJoinTable avoids
	// the bypass entirely.
	if isAnonymousStructDest(db.Statement.Dest) {
		return "anonymous-dest"
	}
	if isMapDest(db.Statement.Dest) {
		return "map-dest"
	}
	// gob omits zero-valued fields, and gorm.Scan leaves fields the query
	// didn't select untouched — so a pre-populated Dest would leak its
	// values into the cached payload (and vice versa on a hit).
	if !destIsBlank(db.Statement.Dest) {
		return "dest-not-blank"
	}
	return ""
}

// multiTableTokenRe matches SQL keywords that pull in another relation.
var multiTableTokenRe = regexp.MustCompile(`(?i)\b(FROM|JOIN)\b`)

// uncacheableQuery returns why an already-built query must not be cached,
// or "" when it may be. Cached entries are tagged with Statement.Table
// only, so any query that reads another table (joins, subqueries, raw
// SQL) would go stale when that other table changes.
func uncacheableQuery(db *gorm.DB, raw bool) string {
	stmt := db.Statement
	if raw {
		return "raw-sql"
	}
	if len(stmt.Joins) > 0 {
		return "joins"
	}
	// SELECT ... FOR UPDATE/SHARE must reach the database to take its lock.
	if _, ok := stmt.Clauses["FOR"]; ok {
		return "locking"
	}
	froms, joins := 0, 0
	for _, m := range multiTableTokenRe.FindAllString(stmt.SQL.String(), -1) {
		if strings.EqualFold(m, "JOIN") {
			joins++
		} else {
			froms++
		}
	}
	if joins > 0 || froms > 1 {
		return "multi-table"
	}
	return ""
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

// isMapDest reports whether dest is a map (or pointer/slice of maps):
// scanned driver values in a map[string]interface{} don't round-trip
// through the cache with their original types.
func isMapDest(dest interface{}) bool {
	t := reflect.TypeOf(dest)
	for t != nil && (t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array) {
		t = t.Elem()
	}
	return t != nil && t.Kind() == reflect.Map
}

// destIsBlank reports whether dest holds no pre-populated struct values.
// Slices and scalars count as blank because gorm.Scan resets them.
func destIsBlank(dest interface{}) bool {
	v := reflect.ValueOf(dest)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return true
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Struct {
		return v.IsZero()
	}
	return true
}

// queryReplace replaces GORM's default gorm:query callback. On cache hit
// it deserializes directly into Dest. On miss it executes the DB query
// (identically to the stock callback) and then caches the result.
func (p *Plugin) queryReplace(db *gorm.DB) {
	if p.cache == nil || db.Statement == nil || db.Error != nil {
		callbacks.Query(db)
		return
	}
	if reason := p.skipReason(db); reason != "" {
		p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: reason})
		callbacks.Query(db)
		return
	}

	// db.Raw(...).Find(...) arrives with its SQL already set.
	raw := db.Statement.SQL.Len() > 0

	// Let GORM build the SQL (parse model, apply scopes, build clauses).
	callbacks.BuildQuerySQL(db)
	if db.Error != nil {
		return
	}

	if reason := uncacheableQuery(db, raw); reason != "" {
		p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: reason, SQL: db.Statement.SQL.String()})
		runQuery(db)
		return
	}

	schemaVer := p.resolveSchemaVersion(db)
	key := p.cacheKey(db.Statement, schemaVer)
	ctx := db.Statement.Context
	label := modelLabel(db.Statement)

	// Try cache.
	data, hit, err := p.cache.Get(ctx, key)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] Get error for %s: %v", key, err)
		}
		p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "get", Err: err})
		// Fall through to DB.
		p.execAndCache(db, key, label, schemaVer)
		return
	}

	if hit {
		if err := decode(data, db.Statement.Dest); err != nil {
			if p.options.debug {
				log.Printf("[cache] decode error for %s: %v", key, err)
			}
			p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "decode", Err: err})
			// Corrupted — reset whatever the failed decode wrote, then
			// run the actual query and re-cache.
			resetDest(db.Statement.Dest)
			p.execAndCache(db, key, label, schemaVer)
			return
		}
		p.options.metrics.Hit(label)
		p.emit(ctx, Event{Kind: EventHit, Model: label, Key: key})
		db.RowsAffected = countRows(db.Statement.Dest)
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
	p.emit(ctx, Event{Kind: EventMiss, Model: label, Key: key, SQL: db.Statement.SQL.String()})
	p.execAndCache(db, key, label, schemaVer)
}

// runQuery executes the already-built query and scans the results,
// mirroring gorm/callbacks.Query without rebuilding the SQL.
func runQuery(db *gorm.DB) {
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
}

// execAndCache executes the already-built query, scans results, and
// populates the cache.
func (p *Plugin) execAndCache(db *gorm.DB, key, label, schemaVer string) {
	if db.DryRun || db.Error != nil {
		return
	}
	runQuery(db)

	if p.cache == nil {
		return
	}
	if db.Error != nil {
		// Includes gorm.ErrRecordNotFound: not-found results are never cached.
		p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: label, Key: key, Reason: "query-error", Err: db.Error})
		return
	}

	ctx := db.Statement.Context
	if _, inTx := txState(db); inTx && ResolveTxMode(db, p.options.defaultTxMode) != TxFull {
		// A value read inside a transaction can be overtaken by another
		// transaction's committed write before this one ends; storing it
		// (now or at commit) could re-cache that stale snapshot.
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "tx-read"})
		return
	}

	data, err := encode(db.Statement.Dest)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] encode error for %s: %v", key, err)
		}
		p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "encode", Err: err})
		return
	}

	var tags []string
	if p.options.tagStrategy != nil {
		tags = p.options.tagStrategy.ReadTags(db, schemaVer)
	} else {
		tags = p.tagsForStatement(db)
	}
	if setErr := p.set(ctx, key, label, data, tags, p.resolveTTL(db)); setErr != nil {
		if p.options.debug {
			log.Printf("[cache] Set error for %s: %v", key, setErr)
		}
	}
}

// writeCallback returns a GORM callback that invalidates cache entries
// after a successful create/update/delete.
func (p *Plugin) writeCallback(reason string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if p.cache == nil || db.Statement == nil || db.Error != nil || db.DryRun {
			return
		}
		p.invalidateForWrite(db, reason)
	}
}

// execWriteRe captures the target table of a raw INSERT/UPDATE/DELETE/
// TRUNCATE/REPLACE statement.
var execWriteRe = regexp.MustCompile(`(?is)^\s*(?:insert\s+(?:or\s+\w+\s+)?into|replace\s+into|update(?:\s+only)?|delete\s+from|truncate(?:\s+table)?(?:\s+only)?)\s+([^\s(,;]+)`)

// execLooksLikeWriteRe flags raw statements that probably write rows even
// though execWriteRe couldn't find their table (e.g. a CTE-prefixed write).
var execLooksLikeWriteRe = regexp.MustCompile(`(?is)^\s*(with|insert|update|delete|truncate|replace|merge)\b`)

// execWriteTable returns the unquoted, unqualified table a raw write
// statement targets, if it can be determined.
func execWriteTable(sql string) (string, bool) {
	m := execWriteRe.FindStringSubmatch(sql)
	if m == nil {
		return "", false
	}
	name := m[1]
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.Trim(name, "\"`[]")
	return name, name != ""
}

// execWriteCallback invalidates after a successful db.Exec that writes.
func (p *Plugin) execWriteCallback(db *gorm.DB) {
	if p.cache == nil || db.Statement == nil || db.Error != nil || db.DryRun {
		return
	}
	sql := db.Statement.SQL.String()
	table, ok := execWriteTable(sql)
	if !ok {
		if execLooksLikeWriteRe.MatchString(sql) {
			p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: "exec-unknown-table", SQL: sql})
		}
		return
	}
	if db.Statement.Table == "" {
		db.Statement.Table = table
	}
	p.invalidateForWrite(db, "exec")
}

// invalidateForWrite derives the write's tags and invalidates them: on
// commit when the surrounding transaction exposes a CommitHook, inline
// otherwise.
func (p *Plugin) invalidateForWrite(db *gorm.DB, reason string) {
	var tags []string
	if p.options.tagStrategy != nil {
		tags = p.options.tagStrategy.WriteTags(db, p.resolveSchemaVersion(db))
	} else {
		tags = p.tagsForWrite(db)
	}
	if len(tags) == 0 {
		return
	}

	ctx := db.Statement.Context
	label := modelLabel(db.Statement)

	// Mark pending writes for dirty-state tracking.
	if pt := lookupPendingTracker(db); pt != nil {
		pt.MarkPending()
	}

	// Always defer when a hook exists, whatever the TxMode: invalidating
	// before commit lets a concurrent reader re-cache the old row, which
	// then outlives the commit.
	if hook, _ := txState(db); hook != nil {
		hook.OnCommit(func(commitCtx context.Context) error {
			p.options.metrics.Invalidation(label, reason)
			err := p.invalidate(commitCtx, label, reason, tags)
			if err != nil {
				p.options.metrics.PostCommitError()
			}
			return err
		})
		return
	}

	p.options.metrics.Invalidation(label, reason)
	if err := p.invalidate(ctx, label, reason, tags); err != nil {
		if p.options.debug {
			log.Printf("[cache] Invalidate error: %v", err)
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
