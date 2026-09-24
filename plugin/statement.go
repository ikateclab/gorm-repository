package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// keyPrefix marks a data key as written by this (Go) side of the cache,
// mirroring the "nodecache:" prefix the Node.js backend puts on its own
// data keys — each language's data keys are never read by the other, but
// carrying a matching kind of marker keeps a `redis-cli SCAN` readable
// about which side wrote what.
const keyPrefix = "gocache:"

// CacheKey derives a deterministic cache key from a GORM statement, in the
// form "gocache:{schemaVersion:}{table}:{hash}" — schemaVersion and table
// kept readable for inspection, the hash (SQL + vars + dest type)
// covering everything that actually varies between queries on that table.
func CacheKey(stmt *gorm.Statement, schemaVersion string) string {
	var b strings.Builder
	if schemaVersion != "" {
		b.WriteString(schemaVersion)
		b.WriteByte('|')
	}
	table := ""
	if stmt != nil {
		table = stmt.Table
		b.WriteString(stmt.Table)
		b.WriteByte('|')
		b.WriteString(stmt.SQL.String())
		b.WriteByte('|')
		b.WriteString(destSignature(stmt.Dest))
		b.WriteByte('|')
		b.WriteString(varsSignature(stmt.Vars))
	}
	sum := sha256.Sum256([]byte(b.String()))

	var key strings.Builder
	key.WriteString(keyPrefix)
	if schemaVersion != "" {
		key.WriteString(schemaVersion)
		key.WriteByte(':')
	}
	if table == "" {
		table = "no-table"
	}
	key.WriteString(table)
	key.WriteByte(':')
	key.WriteString(hex.EncodeToString(sum[:]))
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
	if len(vars) == 0 {
		return ""
	}
	var b strings.Builder
	for i, v := range vars {
		if i > 0 {
			b.WriteByte(',')
		}
		writeVar(&b, v)
	}
	return b.String()
}

// writeVar handles the common scalar types that appear in WHERE clauses.
// Non-trivial values (structs, maps) fall through to fmt's %v with a
// type prefix; that's not ideal long-term but is acceptable while we
// surface real query shapes through tests.
func writeVar(b *strings.Builder, v interface{}) {
	if v == nil {
		b.WriteString("<nil>")
		return
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			b.WriteString("<nil>")
			return
		}
		writeVar(b, rv.Elem().Interface())
	case reflect.Slice, reflect.Array:
		// Bytes fast-path so []byte parameters don't blow up the key.
		// uuid.UUID and similar fixed-size byte arrays (what FindById's id
		// argument actually is) land here as reflect.Array, not
		// reflect.Slice — and since they arrive via an interface{} bound
		// var, the reflect.Value is never addressable, so rv.Bytes()
		// panics ("reflect.Value.Bytes of unaddressable byte array").
		// Copy element-by-element for arrays instead of relying on it.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b.WriteByte('[')
			if rv.Kind() == reflect.Slice {
				b.WriteString(hex.EncodeToString(rv.Bytes()))
			} else {
				buf := make([]byte, rv.Len())
				for i := 0; i < rv.Len(); i++ {
					buf[i] = byte(rv.Index(i).Uint())
				}
				b.WriteString(hex.EncodeToString(buf))
			}
			b.WriteByte(']')
			return
		}
		b.WriteByte('[')
		for i := 0; i < rv.Len(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			writeVar(b, rv.Index(i).Interface())
		}
		b.WriteByte(']')
	case reflect.String:
		b.WriteString(strconv.Quote(rv.String()))
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(rv.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(rv.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(rv.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(strconv.FormatFloat(rv.Float(), 'g', -1, 64))
	default:
		// Special-case time.Time for stable keys regardless of monotonic
		// clock or location pointer identity.
		if t, ok := v.(time.Time); ok {
			b.WriteString(t.UTC().Format(time.RFC3339Nano))
			return
		}
		// Stable fallback: prefix with the Go type to disambiguate.
		b.WriteString(reflect.TypeOf(v).String())
		b.WriteByte('=')
		b.WriteString(fmt.Sprintf("%v", v))
	}
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

// dedupeSorted returns a sorted, de-duplicated copy of in.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	cp := make([]string, len(in))
	copy(cp, in)
	sort.Strings(cp)
	out := cp[:0]
	var last string
	for i, s := range cp {
		if i == 0 || s != last {
			out = append(out, s)
		}
		last = s
	}
	return out
}
