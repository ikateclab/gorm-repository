package plugin

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type taggedUser struct {
	ID string
}

func (u *taggedUser) CacheTags() []string { return []string{"user:" + u.ID} }

type plainUser struct{ ID string }

func TestTagsFromEntity_Pointer(t *testing.T) {
	tags := TagsFromEntity(&taggedUser{ID: "abc"})
	assert.Equal(t, []string{"user:abc"}, tags)
}

func TestTagsFromEntity_Value(t *testing.T) {
	// Pointer-receiver method must still be discovered via Addr.
	u := taggedUser{ID: "abc"}
	tags := TagsFromEntity(u)
	assert.Equal(t, []string{"user:abc"}, tags)
}

func TestTagsFromEntity_SliceOfPointers(t *testing.T) {
	in := []*taggedUser{{ID: "a"}, {ID: "b"}}
	tags := TagsFromEntity(in)
	sort.Strings(tags)
	assert.Equal(t, []string{"user:a", "user:b"}, tags)
}

func TestTagsFromEntity_SliceOfStructs(t *testing.T) {
	in := []taggedUser{{ID: "a"}, {ID: "b"}}
	tags := TagsFromEntity(in)
	sort.Strings(tags)
	assert.Equal(t, []string{"user:a", "user:b"}, tags)
}

func TestTagsFromEntity_Dedup(t *testing.T) {
	in := []*taggedUser{{ID: "a"}, {ID: "a"}, {ID: "b"}}
	tags := TagsFromEntity(in)
	sort.Strings(tags)
	assert.Equal(t, []string{"user:a", "user:b"}, tags)
}

func TestTagsFromEntity_NotTaggable(t *testing.T) {
	tags := TagsFromEntity(&plainUser{ID: "x"})
	assert.Nil(t, tags)
}

func TestTagsFromEntity_Nil(t *testing.T) {
	assert.Nil(t, TagsFromEntity(nil))
	assert.Nil(t, TagsFromEntity((*taggedUser)(nil)))
}

func TestTagsFromStatement_TableOnly(t *testing.T) {
	stmt := &gorm.Statement{Table: "users"}
	tags := TagsFromStatement(stmt, nil)
	assert.Equal(t, []string{"table:users"}, tags)
}

func TestTagsFromStatement_ScopeColumn_Eq(t *testing.T) {
	stmt := &gorm.Statement{
		Table: "messages",
		Clauses: map[string]clause.Clause{
			"WHERE": {
				Name: "WHERE",
				Expression: clause.Where{
					Exprs: []clause.Expression{
						clause.Eq{Column: clause.Column{Name: "account_id"}, Value: "acct-1"},
					},
				},
			},
		},
	}
	tags := TagsFromStatement(stmt, []string{"account_id"})
	assert.ElementsMatch(t, []string{"table:messages", "account_id:acct-1"}, tags)
}

func TestTagsFromStatement_ScopeColumn_StringColumn(t *testing.T) {
	stmt := &gorm.Statement{
		Table: "messages",
		Clauses: map[string]clause.Clause{
			"WHERE": {
				Name: "WHERE",
				Expression: clause.Where{
					Exprs: []clause.Expression{
						clause.Eq{Column: "account_id", Value: 42},
					},
				},
			},
		},
	}
	tags := TagsFromStatement(stmt, []string{"account_id"})
	assert.ElementsMatch(t, []string{"table:messages", "account_id:42"}, tags)
}

func TestTagsFromStatement_ScopeColumn_INSingle(t *testing.T) {
	stmt := &gorm.Statement{
		Table: "messages",
		Clauses: map[string]clause.Clause{
			"WHERE": {
				Name: "WHERE",
				Expression: clause.Where{
					Exprs: []clause.Expression{
						clause.IN{Column: clause.Column{Name: "account_id"}, Values: []interface{}{"acct-1"}},
					},
				},
			},
		},
	}
	tags := TagsFromStatement(stmt, []string{"account_id"})
	assert.ElementsMatch(t, []string{"table:messages", "account_id:acct-1"}, tags)
}

func TestTagsFromStatement_ScopeColumn_INMulti_NotPromoted(t *testing.T) {
	// Multi-value IN should not produce a single account_id tag — there's
	// no single value that captures the query.
	stmt := &gorm.Statement{
		Table: "messages",
		Clauses: map[string]clause.Clause{
			"WHERE": {
				Name: "WHERE",
				Expression: clause.Where{
					Exprs: []clause.Expression{
						clause.IN{Column: clause.Column{Name: "account_id"}, Values: []interface{}{"a", "b"}},
					},
				},
			},
		},
	}
	tags := TagsFromStatement(stmt, []string{"account_id"})
	assert.Equal(t, []string{"table:messages"}, tags)
}

func TestTagsFromStatement_ScopeColumn_InsideAND(t *testing.T) {
	stmt := &gorm.Statement{
		Table: "messages",
		Clauses: map[string]clause.Clause{
			"WHERE": {
				Name: "WHERE",
				Expression: clause.Where{
					Exprs: []clause.Expression{
						clause.AndConditions{Exprs: []clause.Expression{
							clause.Eq{Column: clause.Column{Name: "deleted"}, Value: false},
							clause.Eq{Column: clause.Column{Name: "account_id"}, Value: "acct-1"},
						}},
					},
				},
			},
		},
	}
	tags := TagsFromStatement(stmt, []string{"account_id"})
	assert.ElementsMatch(t, []string{"table:messages", "account_id:acct-1"}, tags)
}

type queryTaggedUser struct{}

func (q queryTaggedUser) QueryCacheTags(stmt *gorm.Statement) []string {
	return []string{"users:list"}
}

func TestTagsFromStatement_QueryTagger(t *testing.T) {
	stmt := &gorm.Statement{Table: "users", Dest: queryTaggedUser{}}
	tags := TagsFromStatement(stmt, nil)
	assert.ElementsMatch(t, []string{"table:users", "users:list"}, tags)
}

func TestTagsFromStatement_NilStatement(t *testing.T) {
	assert.Nil(t, TagsFromStatement(nil, []string{"account_id"}))
}
