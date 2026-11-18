package gormrepository

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// LastWriteStore keeps the WAL position (LSN) of the latest write per account and table, e.g. Redis keys with a TTL.
type LastWriteStore interface {
	// Mark raises each key to lsn, never lowering it, and keeps it for ttl.
	Mark(ctx context.Context, keys []string, lsn uint64, ttl time.Duration) error
	// LastLsn returns the highest LSN among the keys, or 0 when none is set.
	LastLsn(ctx context.Context, keys []string) (uint64, error)
}

const (
	primaryHint   = "/* pgdog_role: primary */ "
	lsnHintPrefix = "/* pgdog_lsn: "
	globalScope   = "_global"
	anyTable      = "*"
	accountColumn = "accountId"
	accountsTable = "accounts"
	// unknownLsn is beyond any replica, so PgDog reads from the primary.
	unknownLsn = math.MaxUint64
)

var (
	readSQLRe     = regexp.MustCompile(`(?i)^\s*select\b`)
	writeSQLRe    = regexp.MustCompile(`(?i)^\s*(insert|update|delete|merge|truncate|with)\b`)
	writeTableRe  = regexp.MustCompile(`(?i)^\s*(?:insert\s+into|update|delete\s+from)\s+(?:"?\w+"?\.)?"?(\w+)"?`)
	exprColumnsRe = regexp.MustCompile(`^\s*(?:"?\w+"?\.)?"?(\w+)"?\s*=\s*\?\s*$`)
)

// ReplicaRouting tags a read with the LSN of the latest write of its account and tables, so PgDog only
// sends it to a replica that has replayed that write. A nil Store sends every read to the primary.
type ReplicaRouting struct {
	Store LastWriteStore
	// TTL keeps a write's LSN. Past it, only PgDog's lag ban protects the read: TTL must exceed
	// ban_replica_lag plus the ~700 ms PgDog takes to ban.
	TTL time.Duration
	// CurrentLsn returns the primary's WAL position. Defaults to pg_current_wal_insert_lsn().
	CurrentLsn func(ctx context.Context) (uint64, error)
	// LsnReadArgs go to the driver before the args of a read with an LSN. Every LSN makes a distinct SQL, so
	// with pgx pass pgx.QueryExecModeExec to keep those reads out of the prepared statement caches.
	LsnReadArgs []any

	lsnMu    sync.Mutex
	nextLsn  *lsnBatch
	querying bool
}

// lsnBatch is the writes served by one LSN query, all committed before it starts.
type lsnBatch struct {
	done chan struct{}
	lsn  uint64
}

// readAccountKey carries a query's account to its preloads, whose WHERE has only the foreign key.
type readAccountKey struct{}

func (r *ReplicaRouting) Name() string { return "gormrepository:replica_routing" }

func (r *ReplicaRouting) Initialize(db *gorm.DB) error {
	if r.CurrentLsn == nil {
		r.CurrentLsn = func(ctx context.Context) (uint64, error) {
			var lsn string
			if err := db.WithContext(ctx).Raw(primaryHint + "SELECT pg_current_wal_insert_lsn()::text").Scan(&lsn).Error; err != nil {
				return 0, err
			}
			return parseLsn(lsn)
		}
	}
	cb := db.Callback()
	afterCommit := "gorm:commit_or_rollback_transaction"
	steps := []error{
		cb.Query().Before("gorm:query").Register("replica:route_query", r.route),
		cb.Row().Before("gorm:row").Register("replica:route_row", r.route),
		cb.Query().After("gorm:query").Register("replica:drop_query_args", r.dropLsnReadArgs),
		cb.Row().After("gorm:row").Register("replica:drop_row_args", r.dropLsnReadArgs),
		cb.Create().After(afterCommit).Register("replica:mark_create", r.markWrite),
		cb.Update().After(afterCommit).Register("replica:mark_update", r.markWrite),
		cb.Delete().After(afterCommit).Register("replica:mark_delete", r.markWrite),
		cb.Raw().After("gorm:raw").Register("replica:mark_raw", r.markRaw),
	}
	for _, err := range steps {
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *ReplicaRouting) route(db *gorm.DB) {
	if db.Error != nil || db.DryRun || inTransaction(db) {
		return
	}
	callbacks.BuildQuerySQL(db)
	if !readSQLRe.MatchString(db.Statement.SQL.String()) {
		return
	}
	hint := r.readHint(db.Statement)
	if hint == "" {
		return
	}
	sql := hint + db.Statement.SQL.String()
	db.Statement.SQL.Reset()
	db.Statement.SQL.WriteString(sql)
	if strings.HasPrefix(hint, lsnHintPrefix) {
		db.Statement.Vars = slices.Concat(r.LsnReadArgs, db.Statement.Vars)
	}
}

// dropLsnReadArgs runs after the read, so logs and errors show only the query's own vars.
func (r *ReplicaRouting) dropLsnReadArgs(db *gorm.DB) {
	if strings.HasPrefix(db.Statement.SQL.String(), lsnHintPrefix) {
		db.Statement.Vars = db.Statement.Vars[len(r.LsnReadArgs):]
	}
}

func (r *ReplicaRouting) readHint(stmt *gorm.Statement) string {
	accountID, ok := accountFromWhere(stmt)
	if ok {
		stmt.Context = context.WithValue(stmt.Context, readAccountKey{}, accountID)
	} else {
		accountID, ok = scopeValue(stmt.Context.Value(readAccountKey{}), true)
	}
	tables, known := readTables(stmt)
	if r.Store == nil || !ok || !known {
		return primaryHint
	}
	keys := make([]string, 0, 2*(len(tables)+1))
	for _, table := range append(tables, anyTable) {
		keys = append(keys, lastWriteKey(accountID, table), lastWriteKey(globalScope, table))
	}
	lsn, err := r.Store.LastLsn(stmt.Context, keys)
	if err != nil {
		return primaryHint
	}
	if lsn == 0 {
		return ""
	}
	return fmt.Sprintf("%s%X/%X */ ", lsnHintPrefix, lsn>>32, uint32(lsn))
}

func (r *ReplicaRouting) markWrite(db *gorm.DB) {
	if db.Error != nil || db.DryRun || db.Statement.Table == "" {
		return
	}
	accountID, ok := accountFromEntity(db.Statement)
	if !ok {
		accountID, ok = accountFromWhere(db.Statement)
	}
	if !ok {
		accountID = globalScope
	}
	r.mark(db, lastWriteKey(accountID, db.Statement.Table))
}

func (r *ReplicaRouting) markRaw(db *gorm.DB) {
	sql := db.Statement.SQL.String()
	if db.Error != nil || db.DryRun || !writeSQLRe.MatchString(sql) {
		return
	}
	table := anyTable
	if m := writeTableRe.FindStringSubmatch(sql); m != nil {
		table = m[1]
	}
	r.mark(db, lastWriteKey(globalScope, table))
}

// mark records the write with the primary's LSN once it is committed: now, or when the repository Tx commits.
func (r *ReplicaRouting) mark(db *gorm.DB, key string) {
	if r.Store == nil {
		return
	}
	ctx := context.WithoutCancel(db.Statement.Context)
	if v, ok := db.Get(txContextKey); ok {
		if tx, ok := v.(*Tx); ok {
			tx.afterCommitWrites(key, func(keys []string) { _ = r.Store.Mark(ctx, keys, r.lsnAfterCommit(ctx), r.TTL) })
			return
		}
	}
	lsn := uint64(unknownLsn)
	// A plain GORM transaction has no commit hook, so its commit LSN is unknown.
	if !inTransaction(db) {
		lsn = r.lsnAfterCommit(ctx)
	}
	_ = r.Store.Mark(ctx, []string{key}, lsn, r.TTL)
}

// lsnAfterCommit runs one LSN query at a time: writes that commit during it wait for the next one, which serves them all.
func (r *ReplicaRouting) lsnAfterCommit(ctx context.Context) uint64 {
	b := r.joinLsnBatch(ctx)
	<-b.done
	return b.lsn
}

func (r *ReplicaRouting) joinLsnBatch(ctx context.Context) *lsnBatch {
	r.lsnMu.Lock()
	defer r.lsnMu.Unlock()
	if r.nextLsn == nil {
		r.nextLsn = &lsnBatch{done: make(chan struct{})}
	}
	if !r.querying {
		r.querying = true
		go r.queryLsnBatches(ctx)
	}
	return r.nextLsn
}

func (r *ReplicaRouting) queryLsnBatches(ctx context.Context) {
	for {
		r.lsnMu.Lock()
		b := r.nextLsn
		r.nextLsn = nil
		r.querying = b != nil
		r.lsnMu.Unlock()
		if b == nil {
			return
		}
		b.lsn = r.currentLsn(ctx)
		close(b.done)
	}
}

func (r *ReplicaRouting) currentLsn(ctx context.Context) uint64 {
	lsn, err := r.CurrentLsn(ctx)
	if err != nil {
		return unknownLsn
	}
	return lsn
}

// parseLsn reads Postgres' "X/Y" notation.
func parseLsn(text string) (uint64, error) {
	high, low, ok := strings.Cut(text, "/")
	if !ok {
		return 0, fmt.Errorf("invalid LSN %q", text)
	}
	h, err := strconv.ParseUint(high, 16, 32)
	if err != nil {
		return 0, err
	}
	l, err := strconv.ParseUint(low, 16, 32)
	if err != nil {
		return 0, err
	}
	return h<<32 | l, nil
}

func lastWriteKey(accountID, table string) string {
	return "last_write:" + accountID + ":" + table
}

func inTransaction(db *gorm.DB) bool {
	_, ok := db.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

// readTables returns the tables a query reads, or false when unknown (raw SQL, string joins).
func readTables(stmt *gorm.Statement) ([]string, bool) {
	if _, built := stmt.Clauses["FROM"]; !built || stmt.Table == "" {
		return nil, false
	}
	tables := []string{stmt.Table}
	for _, join := range stmt.Joins {
		if stmt.Schema == nil {
			return nil, false
		}
		rel, ok := stmt.Schema.Relationships.Relations[join.Name]
		if !ok {
			return nil, false
		}
		tables = append(tables, rel.FieldSchema.Table)
	}
	return tables, true
}

func scopeColumn(stmt *gorm.Statement) string {
	if stmt.Table == accountsTable {
		return "id"
	}
	return accountColumn
}

func accountFromEntity(stmt *gorm.Statement) (string, bool) {
	if stmt.Schema == nil {
		return "", false
	}
	field := stmt.Schema.LookUpField(scopeColumn(stmt))
	if field == nil {
		return "", false
	}
	rv := reflect.Indirect(stmt.ReflectValue)
	switch rv.Kind() {
	case reflect.Struct:
		return fieldScope(stmt, field, rv)
	case reflect.Slice, reflect.Array:
		var accountID string
		for i := 0; i < rv.Len(); i++ {
			id, ok := fieldScope(stmt, field, reflect.Indirect(rv.Index(i)))
			if !ok || (accountID != "" && id != accountID) {
				return "", false
			}
			accountID = id
		}
		return accountID, accountID != ""
	}
	return "", false
}

func fieldScope(stmt *gorm.Statement, field *schema.Field, rv reflect.Value) (string, bool) {
	value, isZero := field.ValueOf(stmt.Context, rv)
	return scopeValue(value, !isZero)
}

func accountFromWhere(stmt *gorm.Statement) (string, bool) {
	where, ok := stmt.Clauses["WHERE"].Expression.(clause.Where)
	if !ok {
		return "", false
	}
	return findEq(where.Exprs, scopeColumn(stmt))
}

func findEq(exprs []clause.Expression, column string) (string, bool) {
	for _, expr := range exprs {
		switch e := expr.(type) {
		case clause.Eq:
			if columnName(e.Column) == column {
				return scopeValue(e.Value, true)
			}
		case clause.Expr:
			if m := exprColumnsRe.FindStringSubmatch(e.SQL); m != nil && m[1] == column && len(e.Vars) == 1 {
				return scopeValue(e.Vars[0], true)
			}
		case clause.AndConditions:
			if id, ok := findEq(e.Exprs, column); ok {
				return id, true
			}
		}
	}
	return "", false
}

func columnName(column interface{}) string {
	switch c := column.(type) {
	case string:
		return c
	case clause.Column:
		return c.Name
	}
	return ""
}

func scopeValue(value interface{}, present bool) (string, bool) {
	if !present || value == nil {
		return "", false
	}
	if kind := reflect.Indirect(reflect.ValueOf(value)).Kind(); kind == reflect.Slice || kind == reflect.Map {
		return "", false
	}
	id := fmt.Sprint(value)
	return id, id != "" && id != "00000000-0000-0000-0000-000000000000"
}
