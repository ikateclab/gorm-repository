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
	assert.True(t, strings.HasPrefix(k1, keyPrefix+"mp1:no-table:"))
}

func TestCacheKey_Format(t *testing.T) {
	k := CacheKey(makeStmt("t", "S", nil, nil), "")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"mp1:t:"), "key should be prefix-namespaced by codec and table")
	hash := strings.TrimPrefix(k, keyPrefix+"mp1:t:")
	assert.Len(t, hash, 64, "SHA-256 hex digest")
}

func TestCacheKey_ReadablePrefix(t *testing.T) {
	k := CacheKey(makeStmt("users", "S", nil, nil), "v1")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"mp1:v1:users:"))
}

func TestCacheKey_NoTableFallback(t *testing.T) {
	k := CacheKey(nil, "v1")
	assert.True(t, strings.HasPrefix(k, keyPrefix+"mp1:v1:no-table:"))
}

func TestCacheKey_KeyPrefix(t *testing.T) {
	s := makeStmt("users", "S", nil, nil)
	a := New(nil, WithKeyPrefix("svc-a")).cacheKey(s, "v1")
	b := New(nil, WithKeyPrefix("svc-b")).cacheKey(s, "v1")

	assert.True(t, strings.HasPrefix(a, keyPrefix+"svc-a:mp1:v1:users:"))
	assert.NotEqual(t, a, b, "services sharing a cache must not share keys")
	assert.Equal(t, CacheKey(s, "v1"), New(nil).cacheKey(s, "v1"), "no prefix keeps the plain layout")
}

func TestCacheKey_KeyName(t *testing.T) {
	s := makeStmt("users", "S", nil, nil)
	named := New(nil, WithKeyName(func(*gorm.Statement) string { return "User" })).cacheKey(s, "v1")
	unnamed := New(nil, WithKeyName(func(*gorm.Statement) string { return "" })).cacheKey(s, "v1")

	assert.Equal(t, keyPrefix+"mp1:v1:User:"+strings.TrimPrefix(CacheKey(s, "v1"), keyPrefix+"mp1:v1:users:"), named,
		"only the readable segment changes")
	assert.Equal(t, CacheKey(s, "v1"), unnamed, "an empty name falls back to the table")
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

type sigStatus string
type sigStruct struct {
	A int
	B string
}
type sigBytes []byte
type sigByte uint8
type sigTime time.Time

// TestVarsSignature_Golden pins how each kind of var renders: it is hashed
// into every cache key, so a change re-keys the whole cache.
func TestVarsSignature_Golden(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	id2 := uuid.MustParse("ffeeddcc-bbaa-9988-7766-554433221100")
	tm := time.Date(2026, 1, 2, 3, 4, 5, 600700800, time.FixedZone("X", -3*3600))
	s, n := "str", 42
	pn := &n
	cases := []struct {
		name string
		in   interface{}
		want string
	}{
		{"nil", nil, "<nil>"},
		{"string", "a\"b\n", `"a\"b\n"`},
		{"named-string", sigStatus("active"), `"active"`},
		{"int", 7, "7"},
		{"int8", int8(-8), "-8"},
		{"int64", int64(1) << 62, "4611686018427387904"},
		{"uint64", uint64(1) << 63, "9223372036854775808"},
		{"uintptr", uintptr(5), "uintptr=5"},
		{"float32", float32(1.1), "1.100000023841858"},
		{"float64", 3.14159, "3.14159"},
		{"bool", true, "true"},
		{"bytes", []byte{0, 1, 0xfe}, "[0001fe]"},
		{"bytes-nil", []byte(nil), "[]"},
		{"named-bytes", sigBytes{9, 8}, "[0908]"},
		{"named-byte-slice", []sigByte{1, 2}, "[0102]"},
		{"named-byte-array", [2]sigByte{1, 2}, "[0102]"},
		{"long-named-byte-array", [65]sigByte{64: 0xab}, "[" + strings.Repeat("00", 64) + "ab]"},
		{"uuid", id, "[00112233445566778899aabbccddeeff]"},
		{"uuid-ptr", &id, "[00112233445566778899aabbccddeeff]"},
		{"uuid-slice", []uuid.UUID{id, id2}, "[[00112233445566778899aabbccddeeff],[ffeeddccbbaa99887766554433221100]]"},
		{"uuid-array", [2]uuid.UUID{id, id2}, "[[00112233445566778899aabbccddeeff],[ffeeddccbbaa99887766554433221100]]"},
		{"byte-array", [3]byte{1, 2, 3}, "[010203]"},
		{"long-byte-array", [65]byte{64: 0xab}, "[" + strings.Repeat("00", 64) + "ab]"},
		{"time", tm, "2026-01-02T06:04:05.6007008Z"},
		{"time-ptr", &tm, "2026-01-02T06:04:05.6007008Z"},
		{"named-time", sigTime(tm), "2026-01-02T06:04:05.6007008Z"},
		{"named-time-ptr", (*sigTime)(&tm), "2026-01-02T06:04:05.6007008Z"},
		{"time-slice", []time.Time{tm, tm.Add(time.Hour)}, "[2026-01-02T06:04:05.6007008Z,2026-01-02T07:04:05.6007008Z]"},
		{"string-ptr", &s, `"str"`},
		{"int-ptr-ptr", &pn, "42"},
		{"nil-ptr", (*int)(nil), "<nil>"},
		{"iface-slice", []interface{}{nil, 1, "x", id, &n, (*int)(nil)}, `[<nil>,1,"x",[00112233445566778899aabbccddeeff],42,<nil>]`},
		{"nested-slice", [][]int{{1}, {2, 3}}, "[[1],[2,3]]"},
		{"empty-slice", []string{}, "[]"},
		{"struct", sigStruct{A: 1, B: "x"}, "plugin.sigStruct={1 x}"},
		{"struct-ptr", &sigStruct{A: 2}, "plugin.sigStruct={2 }"},
		{"map", map[string]int{"a": 1}, "map[string]int=map[a:1]"},
		{"complex", complex(1, 2), "complex128=(1+2i)"},
	}
	var all []interface{}
	var want []string
	for _, c := range cases {
		assert.Equal(t, c.want, varsSignature([]interface{}{c.in}), c.name)
		all = append(all, c.in)
		want = append(want, c.want)
	}
	assert.Equal(t, strings.Join(want, ","), varsSignature(all))
}

// TestCacheKey_Golden pins whole keys: a change to what's hashed, or how,
// re-keys the whole cache.
func TestCacheKey_Golden(t *testing.T) {
	type goldenStruct struct{}
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	byID := makeStmt("users", `SELECT * FROM "users" WHERE id = $1 ORDER BY "users"."id" LIMIT 1`, []interface{}{id}, &goldenStruct{})
	byAccount := makeStmt("users", `SELECT * FROM "users" WHERE "users"."account_id" = $1`, []interface{}{id}, &[]goldenStruct{})
	in := makeStmt("users", `SELECT * FROM "users" WHERE id IN ($1,$2)`, []interface{}{[]uuid.UUID{id, id}, "x"}, &[]*goldenStruct{})

	assert.Equal(t, "cache:go:mp1:no-table:8712005d831d0d2940e7aa6842590d33727cd2cdd921b1157ad028a4f17555ef", buildCacheKey("", "", "", nil))
	assert.Equal(t, "cache:go:mp1:users:e6958a4462776cf653b9fd3fc185d0756320990bdaf2c770588060ebf1072841", buildCacheKey("", "", "", byID))
	assert.Equal(t, "cache:go:svc:mp1:v2:users:6a678573a77100f798ce036b207618a62f1f78d549c94c7b0511838020e97aba", buildCacheKey("svc", "v2", "", byAccount))
	assert.Equal(t, "cache:go:p:mp1:users:0fb62455335a8723e8a1b7404d85ba8c2df2542781f5acb7f121c6c457d73669", buildCacheKey("p", "", "", in))
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

// WithKeyPrefix("x") and WithSchemaVersion("x") used to render the same
// readable key and the same hash.
func TestCacheKey_PrefixAndSchemaVersionDontCollide(t *testing.T) {
	s := makeStmt("users", "S", nil, nil)
	prefixed := New(nil, WithKeyPrefix("x")).cacheKey(s, "")
	versioned := New(nil).cacheKey(s, "x")

	assert.True(t, strings.HasPrefix(prefixed, keyPrefix+"x:mp1:users:"))
	assert.True(t, strings.HasPrefix(versioned, keyPrefix+"mp1:x:users:"))
	assert.NotEqual(t, strings.TrimPrefix(prefixed, keyPrefix+"x:mp1:users:"), strings.TrimPrefix(versioned, keyPrefix+"mp1:x:users:"))

	// Nor can one field's value spill into the other's.
	assert.NotEqual(t, buildCacheKey("a|schema=b", "", "", s), buildCacheKey("a", "b", "", s))
}

// A per-call SchemaVersionSetting keys and tags the read like a plugin-wide
// WithSchemaVersion would, without touching other calls.
func TestCacheKey_PerCallSchemaVersion(t *testing.T) {
	db, _, _, rec, met := setupScriptedDB(t, WithSchemaVersion("v1"))
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	var a, b, c testUser
	require.NoError(t, db.First(&a, 1).Error)
	assert.True(t, strings.HasPrefix(rec.last(EventMiss).Key, keyPrefix+"mp1:v1:test_users:"))
	require.NoError(t, db.Set(SchemaVersionSetting, "v2").First(&b, 1).Error)
	assert.True(t, strings.HasPrefix(rec.last(EventMiss).Key, keyPrefix+"mp1:v2:test_users:"))
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
