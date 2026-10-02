package plugin

import (
	"reflect"
	"time"

	"gorm.io/gorm/schema"
)

// RegisterModel returns an Option that registers per-model overrides at
// plugin construction time. The table name is inferred from T's Tabler
// implementation or from GORM's default naming strategy.
//
// Usage:
//
//	plugin.New(cache,
//	    plugin.RegisterModel[User](plugin.ModelWithTTL(5*time.Minute)),
//	    plugin.RegisterModel[AuditLog](plugin.ModelWithBypass()),
//	)
func RegisterModel[T any](mopts ...ModelOption) Option {
	return func(o *options) {
		mo := ModelOptions{}
		for _, fn := range mopts {
			fn(&mo)
		}
		table := tableNameFor[T]()
		if o.modelRegistrations == nil {
			o.modelRegistrations = make(map[string]*ModelOptions)
		}
		o.modelRegistrations[table] = &mo
	}
}

// ModelOption configures a per-model registration.
type ModelOption func(*ModelOptions)

// ModelWithTTL sets a per-model TTL override.
func ModelWithTTL(ttl time.Duration) ModelOption {
	return func(mo *ModelOptions) { mo.TTL = ttl }
}

// ModelWithBypass causes all queries on this model to skip the cache.
func ModelWithBypass() ModelOption {
	return func(mo *ModelOptions) { mo.Bypass = true }
}

// ModelWithExtraTags adds extra tags that should be included in every
// cache entry for this model.
func ModelWithExtraTags(tags ...string) ModelOption {
	return func(mo *ModelOptions) { mo.ExtraTags = append(mo.ExtraTags, tags...) }
}

// tableNameFor infers the table name for T. If T implements Tabler
// (TableName() string) it uses that; otherwise it falls back to GORM's
// default naming strategy (snake_case plural).
func tableNameFor[T any]() string {
	var t T
	// Check if T itself (value receiver) implements Tabler.
	if tabler, ok := any(t).(interface{ TableName() string }); ok {
		return tabler.TableName()
	}
	// Check pointer receiver.
	if tabler, ok := any(&t).(interface{ TableName() string }); ok {
		return tabler.TableName()
	}
	// Fallback: use GORM's naming strategy.
	rt := reflect.TypeOf(t)
	for rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	ns := schema.NamingStrategy{}
	return ns.TableName(rt.Name())
}
