package gormrepository

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// LastWriteStore remembers recent writes per account and table (e.g. Redis keys with a TTL).
type LastWriteStore interface {
	Mark(ctx context.Context, key string, ttl time.Duration) error
	AnyRecent(ctx context.Context, keys []string) (bool, error)
}

const (
	primaryHint   = "/* pgdog_role: primary */ "
	globalScope   = "_global"
	anyTable      = "*"
	accountColumn = "accountId"
	accountsTable = "accounts"
)

var (
	readSQLRe     = regexp.MustCompile(`(?i)^\s*select\b`)
	writeSQLRe    = regexp.MustCompile(`(?i)^\s*(insert|update|delete|merge|truncate|with)\b`)
	writeTableRe  = regexp.MustCompile(`(?i)^\s*(?:insert\s+into|update|delete\s+from)\s+(?:"?\w+"?\.)?"?(\w+)"?`)
	exprColumnsRe = regexp.MustCompile(`^\s*(?:"?\w+"?\.)?"?(\w+)"?\s*=\s*\?\s*$`)
)

// ReplicaRouting sends a read to the primary, through a PgDog hint, when its account and tables
// were written within TTL. TTL must exceed PgDog's ban_replica_lag + lsn_check_interval.
type ReplicaRouting struct {
	Store LastWriteStore
	TTL   time.Duration
}

func (r *ReplicaRouting) Name() string { return "gormrepository:replica_routing" }

func (r *ReplicaRouting) Initialize(db *gorm.DB) error {
	cb := db.Callback()
	afterCommit := "gorm:commit_or_rollback_transaction"
	steps := []error{
		cb.Query().Before("gorm:query").Register("replica:route_query", r.route),
		cb.Row().Before("gorm:row").Register("replica:route_row", r.route),
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
	if !readSQLRe.MatchString(db.Statement.SQL.String()) || !r.readsFromPrimary(db.Statement) {
		return
	}
	sql := primaryHint + db.Statement.SQL.String()
	db.Statement.SQL.Reset()
	db.Statement.SQL.WriteString(sql)
}

func (r *ReplicaRouting) readsFromPrimary(stmt *gorm.Statement) bool {
	accountID, ok := accountFromWhere(stmt)
	tables, known := readTables(stmt)
	if !ok || !known {
		return true
	}
	keys := make([]string, 0, 2*(len(tables)+1))
	for _, table := range append(tables, anyTable) {
		keys = append(keys, lastWriteKey(accountID, table), lastWriteKey(globalScope, table))
	}
	recent, err := r.Store.AnyRecent(stmt.Context, keys)
	return err != nil || recent
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

// mark records the write now, when GORM has already committed it, or when the repository Tx commits.
func (r *ReplicaRouting) mark(db *gorm.DB, key string) {
	ctx := context.WithoutCancel(db.Statement.Context)
	write := func() { _ = r.Store.Mark(ctx, key, r.TTL) }
	if v, ok := db.Get(txContextKey); ok {
		if tx, ok := v.(*Tx); ok {
			tx.AfterCommit(write)
			return
		}
	}
	// A plain GORM transaction has no commit hook: mark now, as best effort.
	write()
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
