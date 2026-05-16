package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func makeStmt(table, sql string, vars []interface{}, dest interface{}) *gorm.Statement {
	s := &gorm.Statement{Table: table, Vars: vars, Dest: dest}
	s.SQL.WriteString(sql)
	return s
}

func TestCacheKey_Stable(t *testing.T) {
	type User struct{}
	var u User
	a := makeStmt("users", "SELECT * FROM users WHERE id = ?", []interface{}{42}, &u)
	b := makeStmt("users", "SELECT * FROM users WHERE id = ?", []interface{}{42}, &u)
	assert.Equal(t, CacheKey(a, ""), CacheKey(b, ""), "identical inputs must produce identical keys")
}

func TestCacheKey_DifferentVars(t *testing.T) {
	type User struct{}
	var u User
	a := makeStmt("users", "SELECT * FROM users WHERE id = ?", []interface{}{1}, &u)
	b := makeStmt("users", "SELECT * FROM users WHERE id = ?", []interface{}{2}, &u)
	assert.NotEqual(t, CacheKey(a, ""), CacheKey(b, ""))
}

func TestCacheKey_DifferentDest(t *testing.T) {
	type User struct{}
	var u User
	var us []User
	a := makeStmt("users", "SELECT * FROM users", nil, &u)
	b := makeStmt("users", "SELECT * FROM users", nil, &us)
	assert.NotEqual(t, CacheKey(a, ""), CacheKey(b, ""),
		"struct vs slice destinations must produce different keys")
}

func TestCacheKey_SchemaVersion(t *testing.T) {
	type User struct{}
	var u User
	s := makeStmt("users", "SELECT * FROM users", nil, &u)
	assert.NotEqual(t, CacheKey(s, "v1"), CacheKey(s, "v2"))
}

func TestCacheKey_NilStatement(t *testing.T) {
	// Should produce a stable key for nil rather than panicking.
	k1 := CacheKey(nil, "")
	k2 := CacheKey(nil, "")
	assert.Equal(t, k1, k2)
	assert.True(t, strings.HasPrefix(k1, keyPrefix))
}

func TestCacheKey_Format(t *testing.T) {
	k := CacheKey(makeStmt("t", "S", nil, nil), "")
	assert.True(t, strings.HasPrefix(k, keyPrefix), "key should be prefix-namespaced")
	// SHA-256 hex == 64 chars
	assert.Equal(t, len(keyPrefix)+64, len(k))
}

func TestVarsSignature_Types(t *testing.T) {
	cases := []struct {
		name string
		in   []interface{}
	}{
		{"strings", []interface{}{"a", "b", "c"}},
		{"ints", []interface{}{int(1), int8(2), int16(3), int32(4), int64(5)}},
		{"uints", []interface{}{uint(1), uint8(2), uint16(3), uint32(4), uint64(5)}},
		{"floats", []interface{}{1.5, float32(2.5)}},
		{"bool", []interface{}{true, false}},
		{"slice", []interface{}{[]int{1, 2, 3}}},
		{"bytes", []interface{}{[]byte{0xde, 0xad, 0xbe, 0xef}}},
		{"nil-pointer", []interface{}{(*int)(nil)}},
		{"time", []interface{}{time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s1 := varsSignature(c.in)
			s2 := varsSignature(c.in)
			assert.Equal(t, s1, s2)
			assert.NotEmpty(t, s1)
		})
	}
}

func TestModelLabel(t *testing.T) {
	type User struct{}
	var u User
	var us []*User
	assert.Equal(t, "User", modelLabel(makeStmt("users", "", nil, &u)))
	assert.Equal(t, "User", modelLabel(makeStmt("users", "", nil, &us)))
	assert.Equal(t, "users", modelLabel(makeStmt("users", "", nil, nil)))
	assert.Equal(t, "", modelLabel(nil))
}

func TestDedupeSorted(t *testing.T) {
	out := dedupeSorted([]string{"b", "a", "b", "c", "a"})
	assert.Equal(t, []string{"a", "b", "c"}, out)
	assert.Nil(t, dedupeSorted(nil))
}
