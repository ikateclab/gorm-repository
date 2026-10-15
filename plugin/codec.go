package plugin

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"

	"github.com/vmihailenco/msgpack/v5"
)

// codecVersion is mixed into CacheKey's hash, so entries written by a
// different encoding are never decoded by this one.
const codecVersion = "mp1"

// Every cached payload starts with one of these bytes.
const (
	payloadValue    byte = 'g' // the query's RowsAffected (uvarint), then the msgpack-encoded Dest
	payloadNotFound byte = 'n' // a query that found nothing into a single struct (First/Take/Last, Find)
)

// notFound is what a read that found no row stores: the hit raises
// gorm.ErrRecordNotFound again when the query asks for it (First/Take/Last).
var notFound = []byte{payloadNotFound}

// encode serializes a query result for the cache. msgpack is used instead
// of encoding/json because it ignores json tags: a field hidden from API
// output with json:"-" (or reshaped by a MarshalJSON method) must still
// round-trip exactly as gorm.Scan populated it. Unlike encoding/gob it
// also keeps a non-nil pointer to a zero value (*bool -> false) and an
// empty-but-not-nil slice, both of which gorm.Scan produces.
//
// It serializes the Go value, so a type with no exported fields (e.g. a
// Scanner column backed by private fields, like a decimal) would cache as
// an empty value; gorm.Scan fills exported fields, so models are safe, but
// such a column type must not be cached without its own msgpack.Marshaler.
//
// rowsAffected is stored too: it can't be recomputed from v (a grouped
// Count scans one row per group into a single *int64), and a hit must
// report what the query did.
func encode(v interface{}, rowsAffected int64) ([]byte, error) {
	if rowsAffected < 0 {
		rowsAffected = 0
	}
	// The value is encoded straight after the header, into one buffer.
	var buf bytes.Buffer
	var header [1 + binary.MaxVarintLen64]byte
	header[0] = payloadValue
	n := binary.PutUvarint(header[1:], uint64(rowsAffected))
	buf.Write(header[:1+n])
	enc := msgpack.GetEncoder()
	enc.Reset(&buf)
	err := enc.Encode(v)
	msgpack.PutEncoder(enc)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decode deserializes a cached result into dest, which must be blank (see
// destIsBlank and resetDest): a slice is decoded into fresh elements, not
// the ones it already holds. found is false for a cached not-found, which
// leaves dest untouched.
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
		return true, int64(n), msgpack.Unmarshal(data[1+size:], dest)
	default:
		return false, 0, fmt.Errorf("cache: unknown payload marker %q", data[0])
	}
}

// resetDest zeroes the value dest ultimately points to, keeping any
// intermediate pointers intact so Statement.ReflectValue still aliases it.
// A slice becomes nil, so the decoder allocates fresh elements instead of
// decoding into ones the caller may still hold from an earlier query.
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
