package plugin

import (
	"bytes"
	"encoding/gob"
	"reflect"
	"time"
)

// codecVersion is mixed into CacheKey's hash, so entries written by a
// different encoding are never decoded by this one.
const codecVersion = "gob1"

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
func encode(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decode deserializes a cached result into dest, which must be blank
// (see destIsBlank): gob doesn't transmit zero-valued fields.
func decode(data []byte, dest interface{}) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(dest)
}

// resetDest zeroes the value dest ultimately points to, keeping any
// intermediate pointers intact so Statement.ReflectValue still aliases it.
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
