package plugin

import "context"

// CommitHook lets the plugin defer work to a transaction boundary without
// importing or depending on any particular Tx implementation. The
// repository's Tx will implement this interface in a later phase; for now
// the type is defined here so tests and third-party consumers can provide
// their own adapters.
type CommitHook interface {
	// OnCommit registers fn to run when the surrounding transaction
	// commits successfully. Registered functions run in registration
	// order.
	OnCommit(fn func(context.Context) error)
	// OnRollback registers fn to run when the surrounding transaction
	// rolls back. Implementations MAY choose to drop registered
	// functions entirely — fn is invoked primarily to let callers
	// release any resources they allocated in anticipation of commit.
	OnRollback(fn func())
}

// PendingTracker is an optional interface implemented by tx-scoped
// sessions so reads can detect dirty state and bypass the cache
// regardless of TxMode. The plugin will prefer a DB read over a cache
// read whenever HasPendingWrites reports true.
type PendingTracker interface {
	MarkPending()
	HasPendingWrites() bool
}

// ctxKey is an unexported type used as the key for context and
// gorm.Statement settings. Declaring a private type prevents collisions
// with keys defined in other packages.
type ctxKey int

const (
	// CommitHookKey is the context.Value / db.Set key under which a
	// CommitHook is advertised to the plugin.
	CommitHookKey ctxKey = iota + 1

	// PendingTrackerKey is the context.Value / db.Set key under which a
	// PendingTracker is advertised to the plugin.
	PendingTrackerKey
)

// LookupCommitHook extracts a CommitHook from either the context or the
// current gorm.Statement. Callers (the plugin's write callbacks) use this
// to decide whether to invalidate inline or defer.
//
// It is defined here rather than inline at the call site so tests can
// exercise the discovery logic independently.
func LookupCommitHook(ctx context.Context, settings func(interface{}) (interface{}, bool)) (CommitHook, bool) {
	if ctx != nil {
		if v := ctx.Value(CommitHookKey); v != nil {
			if h, ok := v.(CommitHook); ok {
				return h, true
			}
		}
	}
	if settings != nil {
		if v, ok := settings(CommitHookKey); ok {
			if h, ok := v.(CommitHook); ok {
				return h, true
			}
		}
	}
	return nil, false
}
