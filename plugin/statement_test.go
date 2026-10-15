package plugin

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ikateclab/gorm-repository/plugin/memory"
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

func TestCacheKey_KeyPrefix(t *testing.T) {
	s := makeStmt("users", "S", nil, nil)
	a := New(nil, WithKeyPrefix("svc-a")).cacheKey(s, "v1")
	b := New(nil, WithKeyPrefix("svc-b")).cacheKey(s, "v1")

	assert.True(t, strings.HasPrefix(a, keyPrefix+"svc-a:v1:users:"))
	assert.NotEqual(t, a, b, "services sharing a cache must not share keys")
	assert.Equal(t, CacheKey(s, "v1"), New(nil).cacheKey(s, "v1"), "no prefix keeps the plain layout")
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

// WithKeyPrefix("x") and WithSchemaVersion("x") render the same readable
// key; the hash used to be identical too.
func TestCacheKey_PrefixAndSchemaVersionDontCollide(t *testing.T) {
	s := makeStmt("users", "S", nil, nil)
	prefixed := New(nil, WithKeyPrefix("x")).cacheKey(s, "")
	versioned := New(nil).cacheKey(s, "x")

	assert.True(t, strings.HasPrefix(prefixed, keyPrefix+"x:users:"))
	assert.True(t, strings.HasPrefix(versioned, keyPrefix+"x:users:"), "the readable part is unchanged")
	assert.NotEqual(t, prefixed, versioned)

	// Nor can one field's value spill into the other's.
	assert.NotEqual(t, buildCacheKey("a|schema=b", "", s), buildCacheKey("a", "b", s))
}

// A per-call SchemaVersionSetting keys and tags the read like a plugin-wide
// WithSchemaVersion would, without touching other calls.
func TestCacheKey_PerCallSchemaVersion(t *testing.T) {
	db, _, _, rec, met := setupScriptedDB(t, WithSchemaVersion("v1"))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	var a, b, c testUser
	require.NoError(t, db.First(&a, 1).Error)
	assert.True(t, strings.HasPrefix(rec.last(EventMiss).Key, keyPrefix+"v1:test_users:"))
	require.NoError(t, db.Set(SchemaVersionSetting, "v2").First(&b, 1).Error)
	assert.True(t, strings.HasPrefix(rec.last(EventMiss).Key, keyPrefix+"v2:test_users:"))
	require.NoError(t, db.Set(SchemaVersionSetting, "v2").First(&c, 1).Error)
	assert.Equal(t, 2, met.misses)
	assert.Equal(t, 1, met.hits)
}

// Two services sharing one backend under different WithKeyPrefix values
// never read each other's entries, but share invalidation: tags aren't
// prefixed.
func TestCacheKey_PrefixesShareOneBackend(t *testing.T) {
	mc := memory.New()
	open := func(prefix string) (*gorm.DB, *testMetrics) {
		met := &testMetrics{}
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, db.Use(New(mc, WithKeyPrefix(prefix), WithMetrics(met))))
		require.NoError(t, db.AutoMigrate(&testUser{}))
		require.NoError(t, db.Create(&testUser{ID: 1, Name: prefix}).Error)
		return db, met
	}
	dbA, metA := open("svc-a")
	dbB, metB := open("svc-b")

	var a, b testUser
	require.NoError(t, dbA.First(&a, 1).Error)
	require.NoError(t, dbB.First(&b, 1).Error)
	assert.Equal(t, "svc-a", a.Name)
	assert.Equal(t, "svc-b", b.Name)
	assert.Equal(t, 1, metA.misses)
	assert.Equal(t, 1, metB.misses, "svc-b must not be served svc-a's entry")
	assert.Equal(t, 2, mc.Len())

	require.NoError(t, dbA.Model(&testUser{ID: 1}).Update("name", "svc-a2").Error)
	assert.Equal(t, 0, mc.Len(), "a write through one prefix invalidates both")
}
