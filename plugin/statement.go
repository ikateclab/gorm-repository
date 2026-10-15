package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// keyPrefix marks a data key as written by this (Go) side of the cache,
// mirroring the "cache:node:" prefix the Node.js backend puts on its own
// data keys — each language's data keys are never read by the other, but
// a matching marker keeps a `redis-cli SCAN` readable about which side
// wrote what.
const keyPrefix = "cache:go:"

// CacheKey derives a deterministic cache key from a GORM statement, in the
// form "cache:go:{schemaVersion:}{table}:{hash}" — schemaVersion and table
// kept readable for inspection, the hash (codec + SQL + vars + dest type)
// covering everything that actually varies between queries on that table.
func CacheKey(stmt *gorm.Statement, schemaVersion string) string {
	return buildCacheKey("", schemaVersion, stmt)
}

// cacheKey is CacheKey plus the plugin's WithKeyPrefix namespace.
func (p *Plugin) cacheKey(stmt *gorm.Statement, schemaVersion string) string {
	return buildCacheKey(p.options.keyPrefix, schemaVersion, stmt)
}

// keyBufPool holds the scratch buffers buildCacheKey hashes, so a key
// costs no allocation beyond the returned string. maxPooledKeyBuf keeps a
// rare huge query (a long IN list) from pinning its buffer in the pool.
var keyBufPool = sync.Pool{New: func() interface{} { b := make([]byte, 0, 512); return &b }}

const maxPooledKeyBuf = 64 << 10

// buildCacheKey renders "cache:go:{prefix:}{schemaVersion:}{table}:{hash}".
func buildCacheKey(prefix, schemaVersion string, stmt *gorm.Statement) string {
	// prefix and schemaVersion are labelled: the readable part can't tell
	// WithKeyPrefix("x") from WithSchemaVersion("x"), so the hash must.
	bp := keyBufPool.Get().(*[]byte)
	b := append((*bp)[:0], codecVersion...)
	b = append(b, "|prefix="...)
	b = strconv.AppendQuote(b, prefix)
	b = append(b, "|schema="...)
	b = strconv.AppendQuote(b, schemaVersion)
	b = append(b, '|')
	table := ""
	if stmt != nil {
		table = stmt.Table
		b = append(b, stmt.Table...)
		b = append(b, '|')
		b = append(b, stmt.SQL.String()...)
		b = append(b, '|')
		b = append(b, destSignature(stmt.Dest)...)
		b = append(b, '|')
		b = appendVars(b, stmt.Vars)
	}
	sum := sha256.Sum256(b)
	if cap(b) <= maxPooledKeyBuf {
		*bp = b
		keyBufPool.Put(bp)
	}

	if table == "" {
		table = "no-table"
	}
	var key strings.Builder
	key.Grow(len(keyPrefix) + len(prefix) + len(schemaVersion) + len(table) + 3 + hex.EncodedLen(len(sum)))
	key.WriteString(keyPrefix)
	if prefix != "" {
		key.WriteString(prefix)
		key.WriteByte(':')
	}
	if schemaVersion != "" {
		key.WriteString(schemaVersion)
		key.WriteByte(':')
	}
	key.WriteString(table)
	key.WriteByte(':')
	var digest [2 * sha256.Size]byte
	hex.Encode(digest[:], sum[:])
	key.Write(digest[:])
	return key.String()
}

// destSignature returns a stable label for stmt.Dest, distinguishing
// pointer vs slice vs struct destinations of the same element type so
// that a Find(&user) and a Find(&[]user) get distinct cache entries.
func destSignature(dest interface{}) string {
	if dest == nil {
		return "<nil>"
	}
	t := reflect.TypeOf(dest)
	return t.String()
}

// varsSignature renders the bound parameter list deterministically.
func varsSignature(vars []interface{}) string {
	return string(appendVars(nil, vars))
}

// appendVars appends varsSignature's rendering of vars to b.
func appendVars(b []byte, vars []interface{}) []byte {
	for i, v := range vars {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendVar(b, v)
	}
	return b
}

var (
	timeType = reflect.TypeOf(time.Time{})
	byteType = reflect.TypeOf(byte(0))
)

// appendVar handles the common scalar types that appear in WHERE clauses.
// Non-trivial values (structs, maps) fall through to fmt's %v with a
// type prefix; that's not ideal long-term but is acceptable while we
// surface real query shapes through tests.
//
// The rendering is part of every cache key: changing it re-keys the whole
// cache (TestVarsSignature_Golden pins it).
func appendVar(b []byte, v interface{}) []byte {
	// The common types skip reflection; each renders exactly as the
	// reflect path below would.
	switch x := v.(type) {
	case nil:
		return append(b, "<nil>"...)
	case string:
		return strconv.AppendQuote(b, x)
	case int:
		return strconv.AppendInt(b, int64(x), 10)
	case int64:
		return strconv.AppendInt(b, x, 10)
	case bool:
		return strconv.AppendBool(b, x)
	case time.Time:
		return appendTime(b, x)
	}
	return appendVarValue(b, reflect.ValueOf(v))
}

func appendVarValue(b []byte, rv reflect.Value) []byte {
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return append(b, "<nil>"...)
		}
		return appendVarValue(b, rv.Elem())
	case reflect.Slice, reflect.Array:
		// Bytes are hex-encoded so []byte parameters don't blow up the key.
		// uuid.UUID and similar fixed-size byte arrays (what FindById's id
		// argument actually is) land here as reflect.Array, not
		// reflect.Slice — and one bound directly as an interface{} var
		// isn't addressable, so rv.Bytes() would panic ("reflect.Value.Bytes
		// of unaddressable byte array"). Such an array is copied out first.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b = append(b, '[')
			if rv.Kind() == reflect.Slice || rv.CanAddr() {
				b = hex.AppendEncode(b, rv.Bytes())
			} else {
				var scratch [64]byte
				buf := scratch[:0]
				if rv.Len() > len(scratch) {
					buf = make([]byte, rv.Len())
				} else {
					buf = scratch[:rv.Len()]
				}
				if rv.Type().Elem() == byteType {
					reflect.Copy(reflect.ValueOf(buf), rv)
				} else {
					// reflect.Copy panics unless the element types match
					// exactly: a named uint8 is copied one by one.
					for i := range buf {
						buf[i] = byte(rv.Index(i).Uint())
					}
				}
				b = hex.AppendEncode(b, buf)
			}
			return append(b, ']')
		}
		b = append(b, '[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendVarValue(b, rv.Index(i))
		}
		return append(b, ']')
	case reflect.String:
		return strconv.AppendQuote(b, rv.String())
	case reflect.Bool:
		return strconv.AppendBool(b, rv.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.AppendInt(b, rv.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.AppendUint(b, rv.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.AppendFloat(b, rv.Float(), 'g', -1, 64)
	default:
		// Special-case time.Time, and types defined from it, for stable
		// keys regardless of monotonic clock or location pointer identity:
		// %v would print a named time type's *Location address, which
		// differs between processes.
		if rv.Type().ConvertibleTo(timeType) {
			return appendTime(b, rv.Convert(timeType).Interface().(time.Time))
		}
		// Stable fallback: prefix with the Go type to disambiguate.
		b = append(b, rv.Type().String()...)
		b = append(b, '=')
		return fmt.Appendf(b, "%v", rv.Interface())
	}
}

func appendTime(b []byte, t time.Time) []byte {
	return t.UTC().AppendFormat(b, time.RFC3339Nano)
}

// modelLabel returns a short label suitable for metrics, derived from
// the statement's destination or model. Falls back to the table name.
func modelLabel(stmt *gorm.Statement) string {
	if stmt == nil {
		return ""
	}
	for _, candidate := range []interface{}{stmt.Dest, stmt.Model} {
		if candidate == nil {
			continue
		}
		t := reflect.TypeOf(candidate)
		for t.Kind() == reflect.Ptr || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct {
			return t.Name()
		}
	}
	return stmt.Table
}

// dedupeSorted sorts in in place and returns it de-duplicated; callers
// pass a slice they own.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	slices.Sort(in)
	return slices.Compact(in)
}
