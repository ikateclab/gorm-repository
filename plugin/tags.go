package plugin

import (
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CacheTaggable is implemented by entities that opt in to per-row
// invalidation tags. CacheTags should return stable, identity-bearing
// tags such as "user:<id>".
type CacheTaggable interface {
	CacheTags() []string
}

// QueryTagger is implemented by entities that need to widen tags
// inferred from a query (e.g. an account-level scope) beyond what
// reflection alone can derive.
//
// Implementations receive the in-flight *gorm.Statement and may inspect
// its clauses to decide which tags apply.
type QueryTagger interface {
	QueryCacheTags(stmt *gorm.Statement) []string
}

// TagsFromEntity walks pointer/slice/struct values and collects tags
// from every CacheTaggable element it finds.
//
// Slices of pointers and slices of structs are both supported; an entity
// that implements CacheTaggable on its pointer receiver is correctly
// detected even when reached via a slice of structs (we take Addr).
func TagsFromEntity(v interface{}) []string {
	if v == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string

	add := func(tags []string) {
		for _, t := range tags {
			if t == "" {
				continue
			}
			if _, dup := seen[t]; dup {
				continue
			}
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}

	var walk func(reflect.Value)
	walk = func(rv reflect.Value) {
		if !rv.IsValid() {
			return
		}
		switch rv.Kind() {
		case reflect.Ptr, reflect.Interface:
			if rv.IsNil() {
				return
			}
			if t, ok := asTaggable(rv); ok {
				add(t.CacheTags())
				return
			}
			walk(rv.Elem())
		case reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				walk(rv.Index(i))
			}
		case reflect.Struct:
			if t, ok := asTaggable(rv); ok {
				add(t.CacheTags())
				return
			}
			if rv.CanAddr() {
				if t, ok := asTaggable(rv.Addr()); ok {
					add(t.CacheTags())
					return
				}
			}
			// Take a copy we can address — handles slice-of-struct
			// elements, which aren't addressable in their original slot.
			cp := reflect.New(rv.Type()).Elem()
			cp.Set(rv)
			if t, ok := asTaggable(cp.Addr()); ok {
				add(t.CacheTags())
			}
		}
	}
	walk(reflect.ValueOf(v))
	return out
}

// asTaggable returns the value as CacheTaggable if it satisfies the
// interface either directly or via its pointer.
func asTaggable(rv reflect.Value) (CacheTaggable, bool) {
	if !rv.IsValid() || !rv.CanInterface() {
		return nil, false
	}
	if t, ok := rv.Interface().(CacheTaggable); ok {
		return t, true
	}
	return nil, false
}

// TagsFromStatement derives statement-scoped tags. The result always
// contains a "table:<name>" tag when stmt.Table is set; one
// "<col>:<value>" tag is added for each scope column whose value can be
// extracted from a top-level WHERE clause (Eq or IN with a single
// value); finally, if stmt.Dest implements QueryTagger its tags are
// appended.
func TagsFromStatement(stmt *gorm.Statement, scopeColumns []string) []string {
	if stmt == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(t string) {
		if t == "" {
			return
		}
		if _, ok := seen[t]; ok {
			return
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}

	if stmt.Table != "" {
		add("table:" + stmt.Table)
	}

	for _, col := range scopeColumns {
		if v, ok := extractScopeValue(stmt, col); ok {
			add(fmt.Sprintf("%s:%v", col, v))
		}
	}

	if stmt.Dest != nil {
		if q, ok := stmt.Dest.(QueryTagger); ok {
			for _, t := range q.QueryCacheTags(stmt) {
				add(t)
			}
		}
	}

	return out
}

// extractScopeValue returns the bound value of a top-level WHERE
// equality (or single-element IN) on the given column, if any.
//
// We deliberately don't recurse into OR conditions: an OR over a scope
// column produces multiple legitimate values and there's no single tag
// that captures the query.
func extractScopeValue(stmt *gorm.Statement, column string) (interface{}, bool) {
	if stmt == nil {
		return nil, false
	}
	c, ok := stmt.Clauses["WHERE"]
	if !ok {
		return nil, false
	}
	w, ok := c.Expression.(clause.Where)
	if !ok {
		return nil, false
	}
	for _, expr := range w.Exprs {
		if v, ok := matchClauseExpr(expr, column); ok {
			return v, true
		}
	}
	return nil, false
}

func matchClauseExpr(expr clause.Expression, column string) (interface{}, bool) {
	switch e := expr.(type) {
	case clause.Eq:
		if columnMatches(e.Column, column) {
			return e.Value, true
		}
	case clause.IN:
		if columnMatches(e.Column, column) && len(e.Values) == 1 {
			return e.Values[0], true
		}
	case clause.AndConditions:
		for _, sub := range e.Exprs {
			if v, ok := matchClauseExpr(sub, column); ok {
				return v, true
			}
		}
	}
	return nil, false
}

func columnMatches(col interface{}, target string) bool {
	switch c := col.(type) {
	case string:
		return strings.EqualFold(c, target)
	case clause.Column:
		return strings.EqualFold(c.Name, target)
	}
	return false
}
