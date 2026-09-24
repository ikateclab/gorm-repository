package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
	assert.True(t, strings.HasPrefix(k1, keyPrefix+"no-table:"))
}

func TestCacheKey_Format(t *testing.T) {
	k := CacheKey(makeStmt("t", "S", nil, nil), "")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"t:"), "key should be prefix-namespaced by table")
	hash := strings.TrimPrefix(k, keyPrefix+"t:")
	assert.Len(t, hash, 64, "SHA-256 hex digest")
}

func TestCacheKey_ReadablePrefix(t *testing.T) {
	k := CacheKey(makeStmt("users", "S", nil, nil), "v1")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"v1:users:"))
}

func TestCacheKey_NoTableFallback(t *testing.T) {
	k := CacheKey(nil, "v1")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"v1:no-table:"))
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
		{"uuid", []interface{}{uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")}},
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

// TestVarsSignature_UUID_DoesNotPanic is a regression test for a
// production crash: uuid.UUID is a [16]byte array, and unlike a []byte
// slice, an array value obtained through an interface{} (exactly how
// GORM stores bound query vars) is never addressable — reflect.Value.Bytes
// panics on it ("reflect.Value.Bytes of unaddressable byte array"). This
// hit every cached FindById call, since gorm-repository's FindById takes
// a uuid.UUID id and binds it directly as a WHERE var.
func TestVarsSignature_UUID_DoesNotPanic(t *testing.T) {
	id := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")

	assert.NotPanics(t, func() {
		varsSignature([]interface{}{id})
	})

	// Must also be stable and distinguish different UUIDs, not just avoid
	// panicking.
	other := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	s1 := varsSignature([]interface{}{id})
	s2 := varsSignature([]interface{}{id})
	s3 := varsSignature([]interface{}{other})
	assert.Equal(t, s1, s2)
	assert.NotEqual(t, s1, s3)
}

// TestCacheKey_FindByIdShape_UUID reproduces the exact call shape that
// crashed in production: a Statement whose Vars carries a uuid.UUID, as
// GormRepository[T].FindById builds it.
func TestCacheKey_FindByIdShape_UUID(t *testing.T) {
	type User struct{}
	var u User
	id := uuid.MustParse("f47ac10b-58cc-4372-a567-0e02b2c3d479")
	stmt := makeStmt("users", `SELECT * FROM "users" WHERE id = ? AND "users"."deletedAt" IS NULL`, []interface{}{id}, &u)

	assert.NotPanics(t, func() {
		CacheKey(stmt, "")
	})
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
