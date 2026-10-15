package plugin

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"

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
	if v == nil || !mayHoldTaggable(reflect.TypeOf(v)) {
		// Writes run this on every Create/Update/Delete: a batch of a type
		// that can't hold a CacheTaggable isn't walked row by row.
		return nil
	}
	var tags tagSet

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
				tags.add(t.CacheTags()...)
				return
			}
			walk(rv.Elem())
		case reflect.Slice, reflect.Array:
			if !mayHoldTaggable(rv.Type().Elem()) {
				return
			}
			for i := 0; i < rv.Len(); i++ {
				walk(rv.Index(i))
			}
		case reflect.Struct:
			if t, ok := asTaggable(rv); ok {
				tags.add(t.CacheTags()...)
				return
			}
			if !reflect.PointerTo(rv.Type()).Implements(taggableType) {
				return
			}
			if rv.CanAddr() {
				if t, ok := asTaggable(rv.Addr()); ok {
					tags.add(t.CacheTags()...)
				}
				return
			}
			// Take a copy we can address — handles slice-of-struct
			// elements, which aren't addressable in their original slot.
			cp := reflect.New(rv.Type()).Elem()
			cp.Set(rv)
			if t, ok := asTaggable(cp.Addr()); ok {
				tags.add(t.CacheTags()...)
			}
		}
	}
	walk(reflect.ValueOf(v))
	return tags.list
}

var taggableType = reflect.TypeOf((*CacheTaggable)(nil)).Elem()

// asTaggable returns the value as CacheTaggable if it satisfies the
// interface either directly or via its pointer. The type is checked first:
// boxing a struct to ask would copy it.
func asTaggable(rv reflect.Value) (CacheTaggable, bool) {
	if !rv.IsValid() || !rv.CanInterface() {
		return nil, false
	}
	if rv.Kind() != reflect.Interface && !rv.Type().Implements(taggableType) {
		return nil, false
	}
	if t, ok := rv.Interface().(CacheTaggable); ok {
		return t, true
	}
	return nil, false
}

// mayHoldTaggableCache memoizes mayHoldTaggable per type.
var mayHoldTaggableCache sync.Map // reflect.Type -> bool

// mayHoldTaggable reports whether TagsFromEntity could find a CacheTaggable
// in a value of type t: false means walking it is wasted work.
func mayHoldTaggable(t reflect.Type) bool {
	if v, ok := mayHoldTaggableCache.Load(t); ok {
		return v.(bool)
	}
	ok := typeMayHoldTaggable(t, 0)
	mayHoldTaggableCache.Store(t, ok)
	return ok
}

func typeMayHoldTaggable(t reflect.Type, depth int) bool {
	if depth > 16 {
		// A recursive type (type S []S): give up and walk the value.
		return true
	}
	switch t.Kind() {
	case reflect.Interface:
		return true // the dynamic value decides
	case reflect.Ptr:
		return t.Implements(taggableType) || typeMayHoldTaggable(t.Elem(), depth+1)
	case reflect.Slice, reflect.Array:
		return typeMayHoldTaggable(t.Elem(), depth+1)
	case reflect.Struct:
		return t.Implements(taggableType) || reflect.PointerTo(t).Implements(taggableType)
	}
	return false
}

// tagSet collects distinct, non-empty tags in the order first added. Tag
// lists are short, so it scans the list instead of keeping a map until one
// grows long.
type tagSet struct {
	list []string
	seen map[string]struct{}
}

const tagSetScanLimit = 16

func (s *tagSet) add(tags ...string) {
	for _, t := range tags {
		if t == "" || s.has(t) {
			continue
		}
		s.list = append(s.list, t)
		if s.seen != nil {
			s.seen[t] = struct{}{}
		} else if len(s.list) > tagSetScanLimit {
			s.seen = make(map[string]struct{}, 2*len(s.list))
			for _, t := range s.list {
				s.seen[t] = struct{}{}
			}
		}
	}
}

func (s *tagSet) has(t string) bool {
	if s.seen != nil {
		_, ok := s.seen[t]
		return ok
	}
	return slices.Contains(s.list, t)
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
	var tags tagSet
	if stmt.Table != "" {
		tags.add("table:" + stmt.Table)
	}

	for _, col := range scopeColumns {
		if v, ok := ExtractScopeValue(stmt, col); ok {
			tags.add(fmt.Sprintf("%s:%v", col, v))
		}
	}

	if stmt.Dest != nil {
		if q, ok := stmt.Dest.(QueryTagger); ok {
			tags.add(q.QueryCacheTags(stmt)...)
		}
	}

	return tags.list
}

// ExtractScopeValue returns the bound value of a top-level WHERE equality
// (or single-element IN) on column, from either a structured Where or a
// raw `column = ?` condition. Doesn't recurse into OR (no single value
// would capture it).
func ExtractScopeValue(stmt *gorm.Statement, column string) (interface{}, bool) {
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

// simpleEqExprRe matches a raw-SQL `column = ?` condition (optionally
// double-quoted), as produced by db.Where("id = ?", v) — a clause.Expr,
// not the structured clause.Eq a map/struct Where produces.
var simpleEqExprRe = regexp.MustCompile(`^\s*"?([A-Za-z_][A-Za-z0-9_]*)"?\s*=\s*\?\s*$`)

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
	case clause.Expr:
		if len(e.Vars) == 1 {
			if m := simpleEqExprRe.FindStringSubmatch(e.SQL); m != nil && columnMatches(m[1], column) {
				return e.Vars[0], true
			}
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
