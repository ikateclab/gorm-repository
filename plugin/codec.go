package plugin

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

// codecVersion is mixed into CacheKey's hash, so entries written by a
// different encoding are never decoded by this one.
const codecVersion = "gob3"

// Every cached payload starts with one of these bytes.
const (
	payloadValue    byte = 'g' // the query's RowsAffected (uvarint), then the gob-encoded Dest
	payloadNotFound byte = 'n' // a query that found nothing into a single struct (First/Take/Last, Find)
)

// notFound is what a read that found no row stores: the hit raises
// gorm.ErrRecordNotFound again when the query asks for it (First/Take/Last).
var notFound = []byte{payloadNotFound}

func init() {
	// Types that commonly sit behind interface{} in jsonb-backed fields
	// (e.g. map[string]interface{} decoded from JSON).
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	gob.Register(time.Time{})
}

// encode serializes a query result for the cache. gob is used instead of
// encoding/json because it ignores json tags: a field hidden from API
// output with json:"-" (or reshaped by a MarshalJSON method) must still
// round-trip exactly as gorm.Scan populated it.
//
// rowsAffected is stored too: it can't be recomputed from v (a grouped
// Count scans one row per group into a single *int64), and a hit must
// report what the query did.
func encode(v interface{}, rowsAffected int64) ([]byte, error) {
	if rowsAffected < 0 {
		rowsAffected = 0
	}
	var buf bytes.Buffer
	buf.WriteByte(payloadValue)
	buf.Write(binary.AppendUvarint(nil, uint64(rowsAffected)))
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decode deserializes a cached result into dest, which must be blank
// (see destIsBlank and resetDest): gob doesn't transmit zero-valued
// fields, and decodes into whatever elements a slice already holds.
// found is false for a cached not-found, which leaves dest untouched.
func decode(data []byte, dest interface{}) (found bool, rowsAffected int64, err error) {
	if len(data) == 0 {
		return false, 0, errors.New("cache: empty payload")
	}
	switch data[0] {
	case payloadNotFound:
		return false, 0, nil
	case payloadValue:
		n, size := binary.Uvarint(data[1:])
		if size <= 0 || n > math.MaxInt64 {
			return false, 0, errors.New("cache: malformed row count")
		}
		return true, int64(n), gob.NewDecoder(bytes.NewReader(data[1+size:])).Decode(dest)
	default:
		return false, 0, fmt.Errorf("cache: unknown payload marker %q", data[0])
	}
}

// resetDest zeroes the value dest ultimately points to, keeping any
// intermediate pointers intact so Statement.ReflectValue still aliases it.
// A slice becomes nil, so gob allocates fresh elements instead of decoding
// into ones the caller may still hold from an earlier query.
func resetDest(dest interface{}) {
	v := reflect.ValueOf(dest)
	for v.Kind() == reflect.Ptr && !v.IsNil() {
		if v.Elem().Kind() != reflect.Ptr {
			v.Elem().Set(reflect.Zero(v.Elem().Type()))
			return
		}
		v = v.Elem()
	}
}
