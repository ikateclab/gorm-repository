package plugin

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestDecode_Errors(t *testing.T) {
	valid, err := encode(&testUser{ID: 1, Name: "a"}, 1)
	require.NoError(t, err)
	type other struct{ Unrelated []float64 }
	mismatched, err := encode(&other{Unrelated: []float64{1}}, 1)
	require.NoError(t, err)

	cases := map[string][]byte{
		"empty payload":      nil,
		"unknown marker":     []byte("x"),
		"no row count":       {payloadValue},
		"overlong row count": append([]byte{payloadValue}, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01),
		"garbage gob":        {payloadValue, 0x01, 0xde, 0xad, 0xbe, 0xef},
		"truncated gob":      valid[:len(valid)-3],
		"mismatched type":    mismatched,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			var u testUser
			_, _, err := decode(data, &u)
			assert.Error(t, err)
		})
	}
}

func TestDecode_NotFoundLeavesDestUntouched(t *testing.T) {
	u := testUser{Name: "kept"}
	found, rows, err := decode(notFound, &u)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Zero(t, rows)
	assert.Equal(t, "kept", u.Name)
}

func TestEncode_Errors(t *testing.T) {
	// gob refuses nil pointers inside a slice.
	_, err := encode([]*testUser{nil}, 1)
	assert.Error(t, err)

	_, err = encode(func() {}, 1)
	assert.Error(t, err)
}

// opaqueName scans and stores like a string column but has no exported
// fields, which gob can't encode.
type opaqueName struct{ v string }

func (o *opaqueName) Scan(src interface{}) error {
	o.v = fmt.Sprint(src)
	return nil
}

func (o opaqueName) Value() (driver.Value, error) { return o.v, nil }

type opaqueRow struct {
	ID   uint `gorm:"primarykey"`
	Name opaqueName
}

func (opaqueRow) TableName() string { return "opaque_rows" }

// A result gob can't encode is served from the database and never stored.
func TestCodec_EncodeErrorIsNotCached(t *testing.T) {
	db, mc, rec := setupWALDB(t)
	require.NoError(t, db.AutoMigrate(&opaqueRow{}))
	require.NoError(t, db.Create(&opaqueRow{ID: 1, Name: opaqueName{v: "a"}}).Error)
	before := mc.Len()
	rec.reset()

	for i := 0; i < 2; i++ {
		var row opaqueRow
		require.NoError(t, db.First(&row, 1).Error)
		assert.Equal(t, "a", row.Name.v)
	}
	errs := rec.kindsOf(EventError)
	require.Len(t, errs, 2)
	assert.Equal(t, "encode", errs[0].Reason)
	assert.Error(t, errs[0].Err)
	assert.Empty(t, rec.kindsOf(EventHit))
	assert.Equal(t, before, mc.Len())
}

// A corrupt entry is reported, then replaced by a fresh read.
func TestCodec_DecodeErrorRereadsAndRecaches(t *testing.T) {
	db, mc, rec := setupWALDB(t)
	ctx := t.Context()

	var u testUser
	require.NoError(t, db.Where("name = ?", "before").Find(&u).Error)
	miss := rec.last(EventMiss)
	require.NotEmpty(t, miss.Key)

	// Overwrite the entry under the same key and tags.
	_, _, versions, err := mc.Get(ctx, miss.Key, miss.Tags)
	require.NoError(t, err)
	require.NoError(t, mc.Set(ctx, miss.Key, []byte("garbage"), miss.Tags, 0, versions))
	rec.reset()

	var again testUser
	require.NoError(t, db.Where("name = ?", "before").Find(&again).Error)
	assert.Equal(t, "before", again.Name)
	decodeErr := rec.last(EventError)
	assert.Equal(t, "decode", decodeErr.Reason)
	assert.Equal(t, miss.Key, decodeErr.Key)
	assert.Equal(t, miss.Key, rec.last(EventSet).Key, "the fresh read replaces the corrupt entry")

	rec.reset()
	var hit testUser
	require.NoError(t, db.Where("name = ?", "before").Find(&hit).Error)
	assert.Len(t, rec.kindsOf(EventHit), 1)
	assert.Equal(t, "before", hit.Name)
}

// A hit into a reused []T used to decode into the elements left from the
// last query: a field the cached row leaves zero kept the old value.
func TestCodec_ReusedSliceDest_NoFieldLeak(t *testing.T) {
	db, _, met := setupCachedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	require.NoError(t, db.Create(&testUser{ID: 2, Name: ""}).Error)

	var warm []testUser
	require.NoError(t, db.Where("id = ?", 2).Find(&warm).Error)

	var users []testUser
	require.NoError(t, db.Where("id = ?", 1).Find(&users).Error)
	require.Equal(t, "alice", users[0].Name)
	require.NoError(t, db.Where("id = ?", 2).Find(&users).Error)
	require.Equal(t, 1, met.hits)
	require.Len(t, users, 1)
	assert.Equal(t, uint(2), users[0].ID)
	assert.Equal(t, "", users[0].Name, "no field may leak from the reused slice's old element")
}

// A hit into a reused []*T used to overwrite the structs the caller still
// held from the last query. GORM's own scan allocates new ones.
func TestCodec_ReusedPointerSliceDest_CallerStructsUntouched(t *testing.T) {
	db, _, met := setupCachedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)
	require.NoError(t, db.Create(&testUser{ID: 2, Name: "bob"}).Error)

	var warm []*testUser
	require.NoError(t, db.Where("id = ?", 2).Find(&warm).Error)

	var users []*testUser
	require.NoError(t, db.Where("id = ?", 1).Find(&users).Error)
	alice := users[0]
	require.NoError(t, db.Where("id = ?", 2).Find(&users).Error)
	require.Equal(t, 1, met.hits)
	assert.Equal(t, "bob", users[0].Name)
	assert.Equal(t, "alice", alice.Name, "a hit must not mutate a struct the caller kept")
	assert.Equal(t, uint(1), alice.ID)
}

// A cached not-found empties a reused slice, as gorm.Scan does.
func TestCodec_NotFoundHitEmptiesReusedSlice(t *testing.T) {
	db, _, met := setupCachedDB(t)
	require.NoError(t, db.Create(&testUser{ID: 1, Name: "alice"}).Error)

	var users []testUser
	require.ErrorIs(t, db.Where("id = ?", 9).First(&users).Error, gorm.ErrRecordNotFound)
	require.NoError(t, db.Where("id = ?", 1).Find(&users).Error)
	require.Len(t, users, 1)
	require.ErrorIs(t, db.Where("id = ?", 9).First(&users).Error, gorm.ErrRecordNotFound)
	require.Equal(t, 1, met.hits)
	assert.Empty(t, users)
}

type codecBenchRow struct {
	Id        [16]byte
	Name      string
	Email     string
	Status    string
	Score     int
	CreatedAt time.Time
}

// BenchmarkCodec shows what encoding a result costs on a miss and decoding
// it costs on a hit, for one row and for a 100-row list. JSON, the codec
// gob replaced (it dropped json:"-" fields), is there for reference.
//
//	go test -run '^$' -bench BenchmarkCodec -benchmem ./plugin/
func BenchmarkCodec(b *testing.B) {
	one := codecBenchRow{Name: "user", Email: "user@example.com", Status: "active", Score: 7, CreatedAt: time.Now()}
	list := make([]codecBenchRow, 100)
	for i := range list {
		list[i] = one
		list[i].Score = i
	}

	for _, c := range []struct {
		name string
		v    interface{}
		dest func() interface{}
	}{
		{"one", &one, func() interface{} { return &codecBenchRow{} }},
		{"list100", &list, func() interface{} { return &[]codecBenchRow{} }},
	} {
		data, err := encode(c.v, 1)
		require.NoError(b, err)
		jsonData, err := json.Marshal(c.v)
		require.NoError(b, err)

		b.Run(c.name+"/encode-gob", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := encode(c.v, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(c.name+"/decode-gob", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := decode(data, c.dest()); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(c.name+"/encode-json", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := json.Marshal(c.v); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(c.name+"/decode-json", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := json.Unmarshal(jsonData, c.dest()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
