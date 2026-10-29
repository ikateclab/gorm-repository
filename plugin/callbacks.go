package plugin

import (
	"context"
	"errors"
	"log"
	"reflect"
	"regexp"
	"strings"
	"sync"
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
	rowAfterName     = "gormrepository:cache:after_row"

	// commitOrRollbackName is GORM's callback that commits the implicit
	// per-write transaction (when SkipDefaultTransaction is off).
	commitOrRollbackName = "gorm:commit_or_rollback_transaction"

	// maxReadAge bounds how long after its lookup a read may still store
	// its result: a backend only remembers invalidations for a while, so
	// an older read can't be checked against them.
	maxReadAge        = time.Minute
	invalidateTimeout = 2 * time.Second
)

// nowFunc is the clock execAndCache measures a read's age with; tests
// swap it to simulate a slow read.
var nowFunc = time.Now

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
	// db.Raw(...).Scan/Row/Rows run the Row chain, so an UPDATE ...
	// RETURNING read that way writes too.
	if err := db.Callback().Row().After("gorm:row").Register(rowAfterName, p.execWriteCallback); err != nil {
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
	// gorm.Scan leaves fields the query didn't select untouched, so a
	// pre-populated Dest would leak its values into the cached payload
	// (and vice versa on a hit).
	if !destIsBlank(db.Statement.Dest) {
		return "dest-not-blank"
	}
	return ""
}

// plainTableExprRe matches a TableExpr naming one table, optionally
// schema-qualified and quoted, as GORM writes it for Table("users"),
// Table("public.users") or a schema-qualified TableName. The last
// identifier is captured.
var plainTableExprRe = regexp.MustCompile("^\\s*(?:(?:\\w+|\"[^\"]+\"|`[^`]+`|\\[[^\\]]+\\])\\.)?(\\w+|\"[^\"]+\"|`[^`]+`|\\[[^\\]]+\\])\\s*$")

// plainTableExpr reports whether stmt reads only the table it is tagged
// with. GORM sets TableExpr even for a plain Table("users"); anything else
// there (an alias, a comma join, a subquery) can read another relation, or
// leave Statement.Table naming the alias instead of the table.
func plainTableExpr(stmt *gorm.Statement) bool {
	e := stmt.TableExpr
	if e == nil {
		return true
	}
	if len(e.Vars) > 0 {
		return false
	}
	m := plainTableExprRe.FindStringSubmatch(e.SQL)
	return m != nil && strings.Trim(m[1], "\"`[]") == stmt.Table
}

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
	if !plainTableExpr(stmt) {
		return "table-expr"
	}
	// SELECT ... FOR UPDATE/SHARE must reach the database to take its lock.
	if _, ok := stmt.Clauses["FOR"]; ok {
		return "locking"
	}
	if readsAnotherRelation(stmt.SQL.String()) {
		return "multi-table"
	}
	return ""
}

// readsAnotherRelation reports whether sql has a JOIN, or more than one
// FROM, as a whole word in any case — the keywords that pull in another
// relation. It runs on every cacheable query, so it scans words by hand: a
// `(?i)\b(FROM|JOIN)\b` regexp, which finds the same ones, was the costliest
// step of a cache hit.
func readsAnotherRelation(sql string) bool {
	froms := 0
	for i := 0; i < len(sql); {
		if !isWordByte(sql[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(sql) && isWordByte(sql[j]) {
			j++
		}
		if j-i == 4 {
			switch w := sql[i:j]; {
			case strings.EqualFold(w, "join"):
				return true
			case strings.EqualFold(w, "from"):
				if froms++; froms > 1 {
					return true
				}
			}
		}
		i = j
	}
	return false
}

// isWordByte reports whether c is an ASCII word character, as regexp's \w
// and \b define it.
func isWordByte(c byte) bool {
	return c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
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

// isStructDest reports whether dest holds a single struct (behind any
// pointers), rather than a slice, map or scalar.
func isStructDest(dest interface{}) bool {
	t := reflect.TypeOf(dest)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t != nil && t.Kind() == reflect.Struct
}

// queryReplace replaces GORM's default gorm:query callback. On cache hit
// it deserializes directly into Dest. On miss it executes the DB query
// (identically to the stock callback) and then caches the result.
func (p *Plugin) queryReplace(db *gorm.DB) {
	if p.cache == nil || db.Statement == nil || db.Error != nil {
		callbacks.Query(db)
		return
	}
	// A DryRun only builds the SQL (for ToSQL, say): a hit would serve
	// rows instead and clear that SQL.
	if db.DryRun {
		callbacks.Query(db)
		return
	}

	// db.Raw(...).Find(...) arrives with its SQL already set. It's never
	// cached, but it may write (UPDATE ... RETURNING): whichever path
	// below runs it, check it for a write afterwards.
	raw := db.Statement.SQL.Len() > 0
	if raw {
		defer p.execWriteCallback(db)
	}

	if reason := p.skipReason(db); reason != "" {
		p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: reason})
		callbacks.Query(db)
		return
	}

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
	// Checked after the build: before it, Statement.Table may not be known.
	if hasPendingWrites(db) {
		p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: "pending-writes", SQL: db.Statement.SQL.String()})
		runQuery(db)
		return
	}

	schemaVer := p.resolveSchemaVersion(db)
	key := p.cacheKey(db.Statement, schemaVer)
	ctx := db.Statement.Context
	label := modelLabel(db.Statement)

	// A read's tags come from the query alone. With none, nothing could
	// invalidate its entry: it runs past the cache, without a lookup.
	tags := p.readTags(db, schemaVer)
	if len(tags) == 0 {
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "no-tags"})
		runQuery(db)
		return
	}

	data, hit, versions, err := p.cache.Get(ctx, key, tags)
	if err != nil {
		if p.options.debug {
			log.Printf("[cache] Get error for %s: %v", key, err)
		}
		p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "get", Tags: tags, Err: err})
		// Fall through to DB, without caching: there are no versions to
		// store under.
		runQuery(db)
		return
	}

	if hit {
		// Decode into a blank Dest: the decoder would otherwise reuse the
		// elements a reused slice still holds, mutating structs the caller
		// kept from an earlier query and leaking fields this entry omits.
		resetDest(db.Statement.Dest)
		found, rows, err := decode(data, db.Statement.Dest)
		if err != nil {
			if p.options.debug {
				log.Printf("[cache] decode error for %s: %v", key, err)
			}
			p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "decode", Tags: tags, Err: err})
			// Corrupted — reset whatever the failed decode wrote, then
			// run the actual query and re-cache.
			resetDest(db.Statement.Dest)
			p.execAndCache(db, key, label, tags, versions)
			return
		}
		p.options.metrics.Hit(label)
		p.emit(ctx, Event{Kind: EventHit, Model: label, Key: key, Tags: tags})
		db.RowsAffected = rows
		if db.Statement.Result != nil {
			db.Statement.Result.RowsAffected = rows
		}
		// Like gorm.Scan: no row raises only for First/Take/Last, whichever
		// query shape stored the entry (a Find may share their key).
		if (!found || rows == 0) && db.Statement.RaiseErrorOnNotFound {
			_ = db.AddError(gorm.ErrRecordNotFound)
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
	p.execAndCache(db, key, label, tags, versions)
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
// caches them under tags and the versions the lookup returned.
func (p *Plugin) execAndCache(db *gorm.DB, key, label string, tags []string, versions Versions) {
	if db.DryRun || db.Error != nil {
		return
	}
	readAt := nowFunc()
	runQuery(db)

	if p.cache == nil {
		return
	}
	ctx := db.Statement.Context

	var data []byte
	switch {
	case errors.Is(db.Error, gorm.ErrRecordNotFound),
		db.Error == nil && db.RowsAffected == 0 && isStructDest(db.Statement.Dest):
		// A not-found is cached too: the read's tags cover the row that
		// may come. A Find into a struct that matched nothing stores one
		// as well, or a First/Take sharing its key would be served the
		// blank struct instead of raising.
		data = notFound
	case db.Error != nil:
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "query-error", Tags: tags, Err: db.Error})
		return
	default:
		var err error
		if data, err = encode(db.Statement.Dest, db.RowsAffected); err != nil {
			if p.options.debug {
				log.Printf("[cache] encode error for %s: %v", key, err)
			}
			p.emit(ctx, Event{Kind: EventError, Model: label, Key: key, Reason: "encode", Tags: tags, Err: err})
			return
		}
	}

	if _, inTx := txState(db); inTx && ResolveTxMode(db, p.options.defaultTxMode) != TxFull {
		// Never stored, now or at commit: under REPEATABLE READ or
		// SERIALIZABLE the transaction's snapshot can predate an
		// invalidation that versions already include, so the value could
		// be stale even though nothing invalidated it afterwards.
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "tx-read", Tags: tags})
		return
	}
	if nowFunc().Sub(readAt) > maxReadAge {
		p.emit(ctx, Event{Kind: EventSkip, Model: label, Key: key, Reason: "slow-read", Tags: tags})
		return
	}

	if setErr := p.set(ctx, key, label, data, tags, p.resolveTTL(db), versions); setErr != nil {
		if p.options.debug {
			log.Printf("[cache] Set error for %s: %v", key, setErr)
		}
	}
}

// writeCallback returns a GORM callback that invalidates cache entries
// after a create/update/delete — even one that reports an error: the write
// may have reached the database before a later callback (an After* hook,
// say) failed, and an extra invalidation only costs a miss.
func (p *Plugin) writeCallback(reason string) func(*gorm.DB) {
	return func(db *gorm.DB) {
		if p.cache == nil || db.Statement == nil || db.DryRun {
			return
		}
		p.invalidateForWrite(db, reason)
	}
}

// execWriteRe captures the target table of a raw INSERT/UPDATE/DELETE/
// TRUNCATE/REPLACE statement, including MySQL's priority and IGNORE
// modifiers, SQLite's OR <conflict> and Postgres's ONLY.
var execWriteRe = regexp.MustCompile(`(?is)^\s*(?:` +
	`(?:insert|replace)(?:\s+(?:low_priority|delayed|high_priority))?(?:\s+ignore)?(?:\s+or\s+\w+)?\s+into` +
	`|update(?:\s+(?:only|low_priority))?(?:\s+ignore)?(?:\s+or\s+\w+)?` +
	`|delete(?:\s+(?:low_priority|quick|ignore))*\s+from(?:\s+only)?` +
	`|truncate(?:\s+table)?(?:\s+only)?` +
	`)\s+([^\s(,;]+)`)

// execLooksLikeWriteRe flags raw statements that probably write rows even
// though execWriteRe couldn't find their table (e.g. a CTE-prefixed write).
// A WITH counts only if a DML keyword follows, so a CTE read doesn't.
var execLooksLikeWriteRe = regexp.MustCompile(`(?is)^\s*(?:insert|update|delete|truncate|replace|merge|upsert)\b|^\s*with\b.*\b(?:insert|update|delete|merge)\b`)

// leadingCommentRe matches one block or line comment opening a statement.
var leadingCommentRe = regexp.MustCompile(`(?s)^\s*(?:/\*.*?\*/|--[^\n]*(?:\n|$))`)

// stripLeadingComments drops the comments a statement opens with (query
// tags such as /* app=api */), so the write regexes see its first keyword.
func stripLeadingComments(sql string) string {
	for {
		loc := leadingCommentRe.FindStringIndex(sql)
		if loc == nil {
			return sql
		}
		sql = sql[loc[1]:]
	}
}

// truncateListRe captures the table list of a TRUNCATE, which may name
// several tables, before any Postgres IDENTITY or CASCADE option.
var truncateListRe = regexp.MustCompile(`(?is)^\s*truncate(?:\s+table)?\s+(.+?)(?:\s+(?:restart|continue|cascade|restrict)\b.*)?\s*;?\s*$`)

// execWriteTables returns the unquoted, unqualified tables a raw write
// statement targets, if they can be determined: one for INSERT, UPDATE,
// DELETE and REPLACE, every listed one for TRUNCATE.
func execWriteTables(sql string) []string {
	sql = stripLeadingComments(sql)
	if m := truncateListRe.FindStringSubmatch(sql); m != nil {
		var tables []string
		for _, item := range strings.Split(m[1], ",") {
			// Postgres allows "ONLY t" and "t *" per table.
			fields := strings.Fields(item)
			if len(fields) > 1 && strings.EqualFold(fields[0], "only") {
				fields = fields[1:]
			}
			if len(fields) == 0 {
				continue
			}
			if name := unqualifiedTable(strings.TrimSuffix(fields[0], "*")); name != "" {
				tables = append(tables, name)
			}
		}
		return tables
	}
	m := execWriteRe.FindStringSubmatch(sql)
	if m == nil {
		return nil
	}
	if name := unqualifiedTable(m[1]); name != "" {
		return []string{name}
	}
	return nil
}

// execWriteTable returns the first table execWriteTables finds.
func execWriteTable(sql string) (string, bool) {
	tables := execWriteTables(sql)
	if len(tables) == 0 {
		return "", false
	}
	return tables[0], true
}

// unqualifiedTable strips a schema qualifier and identifier quotes.
func unqualifiedTable(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return strings.Trim(name, "\"`[]")
}

// execWriteCallback invalidates after a raw statement that writes:
// db.Exec (the Raw chain), or an UPDATE ... RETURNING run through
// db.Raw(...).Scan/Row/Rows (the Row chain) or .Find (queryReplace). Like
// writeCallback, it runs even when the statement reported an error.
//
// On the Row chain this runs before the caller has read and closed the
// rows, so an autocommit write may not have committed yet; db.Exec and
// .Find have finished with the statement by now.
func (p *Plugin) execWriteCallback(db *gorm.DB) {
	if p.cache == nil || db.Statement == nil || db.DryRun {
		return
	}
	sql := db.Statement.SQL.String()
	tables := execWriteTables(sql)
	if len(tables) == 0 {
		if execLooksLikeWriteRe.MatchString(stripLeadingComments(sql)) {
			p.emit(db.Statement.Context, Event{Kind: EventSkip, Model: modelLabel(db.Statement), Reason: "exec-unknown-table", SQL: sql})
			// Nothing to invalidate, but every table is dirty now.
			markPending(db, "")
		}
		return
	}
	// The SQL names the tables written. Statement.Table may name another:
	// db.Model(&a).Exec(...), or the Dest a RETURNING scans into.
	prev := db.Statement.Table
	defer func() { db.Statement.Table = prev }()
	for _, table := range tables {
		db.Statement.Table = table
		p.invalidateForWrite(db, "exec")
	}
}

// markPending flags table ("" if unknown) as written in the session's
// transaction, if it tracks that.
func markPending(db *gorm.DB, table string) {
	if pt := lookupPendingTracker(db); pt != nil {
		pt.MarkPending(unqualifiedTable(table))
	}
}

// hasPendingWrites reports whether the session's transaction wrote the
// table this query reads.
func hasPendingWrites(db *gorm.DB) bool {
	pt := lookupPendingTracker(db)
	return pt != nil && pt.HasPendingWrites(unqualifiedTable(db.Statement.Table))
}

// invalidateForWrite derives the write's tags and invalidates them: on
// commit when the surrounding transaction exposes a CommitHook, inline
// otherwise.
func (p *Plugin) invalidateForWrite(db *gorm.DB, reason string) {
	// Mark pending writes for dirty-state tracking, before the no-tags
	// return: a write nothing is tagged for still changed rows a later
	// read in this transaction must not be served from the cache.
	markPending(db, db.Statement.Table)

	var tags []string
	if p.options.tagStrategy != nil {
		tags = p.options.tagStrategy.WriteTags(db, p.resolveSchemaVersion(db))
	} else {
		tags = p.tagsForWrite(db)
	}
	p.InvalidateWrite(db, modelLabel(db.Statement), reason, tags)
}

// InvalidateWrite invalidates the tags of a write the plugin's callbacks
// don't see, as theirs are: on the commit of db's transaction, with its other
// writes, when it exposes a CommitHook; inline otherwise. model and reason
// label its event.
func (p *Plugin) InvalidateWrite(db *gorm.DB, model, reason string, tags []string) {
	if p.cache == nil || len(tags) == 0 {
		return
	}
	w := invalidation{model: model, reason: reason, tags: tags}

	// Always defer when a hook exists, whatever the TxMode: invalidating
	// before commit lets a concurrent reader re-cache the old row, which
	// then outlives the commit.
	if hook, _ := txState(db); hook != nil {
		p.deferInvalidation(hook, w)
		return
	}

	// A write that already reached the database invalidates even if the
	// request that made it is gone.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(db.Statement.Context), invalidateTimeout)
	defer cancel()
	if err := p.invalidate(ctx, w); err != nil {
		if p.options.debug {
			log.Printf("[cache] Invalidate error: %v", err)
		}
	}
}

// txInvalidations are the invalidations a transaction's writes deferred to
// its commit.
type txInvalidations struct {
	mu     sync.Mutex
	writes []invalidation
}

// deferInvalidation adds w to the invalidations hook's transaction sends on
// commit, all in one Invalidate: the hook runs its functions one after
// another, and one per write would cost a round-trip each. Only the first
// write registers one. A hook that can't key a map gets one per write.
func (p *Plugin) deferInvalidation(hook CommitHook, w invalidation) {
	if !reflect.TypeOf(hook).Comparable() {
		hook.OnCommit(func(ctx context.Context) error { return p.invalidateOnCommit(ctx, w) })
		return
	}
	v, loaded := p.txInvalidations.LoadOrStore(hook, &txInvalidations{})
	pending := v.(*txInvalidations)
	if !loaded {
		hook.OnCommit(func(ctx context.Context) error {
			// A write still to come on this hook starts another batch.
			p.txInvalidations.CompareAndDelete(hook, pending)
			pending.mu.Lock()
			writes := pending.writes
			pending.mu.Unlock()
			return p.invalidateOnCommit(ctx, writes...)
		})
		hook.OnRollback(func() { p.txInvalidations.CompareAndDelete(hook, pending) })
	}
	pending.mu.Lock()
	pending.writes = append(pending.writes, w)
	pending.mu.Unlock()
}

func (p *Plugin) invalidateOnCommit(ctx context.Context, writes ...invalidation) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidateTimeout)
	defer cancel()
	err := p.invalidate(ctx, writes...)
	if err != nil {
		p.options.metrics.PostCommitError()
	}
	return err
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
