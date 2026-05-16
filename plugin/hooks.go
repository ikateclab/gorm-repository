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

// ctxKey is an unexported type used as context.Value keys.
type ctxKey int

const (
	// ctxCommitHookKey is the context.Value key for CommitHook.
	ctxCommitHookKey ctxKey = iota + 1

	// ctxPendingTrackerKey is the context.Value key for PendingTracker.
	ctxPendingTrackerKey
)

// String-based keys for GORM's db.Set/db.Get (which require string keys).
const (
	// CommitHookKey is the db.Set key under which a CommitHook is stored.
	CommitHookKey = "cache:commit_hook"

	// PendingTrackerKey is the db.Set key under which a PendingTracker
	// is stored.
	PendingTrackerKey = "cache:pending_tracker"
)

// CommitHookContext returns a context carrying the given CommitHook,
// discoverable via LookupCommitHook.
func CommitHookContext(ctx context.Context, hook CommitHook) context.Context {
	return context.WithValue(ctx, ctxCommitHookKey, hook)
}

// PendingTrackerContext returns a context carrying the given PendingTracker.
func PendingTrackerContext(ctx context.Context, pt PendingTracker) context.Context {
	return context.WithValue(ctx, ctxPendingTrackerKey, pt)
}

// LookupCommitHook extracts a CommitHook from either the context or the
// current gorm session (db.Get). Callers (the plugin's write callbacks)
// use this to decide whether to invalidate inline or defer.
func LookupCommitHook(ctx context.Context, dbGet func(string) (interface{}, bool)) (CommitHook, bool) {
	if ctx != nil {
		if v := ctx.Value(ctxCommitHookKey); v != nil {
			if h, ok := v.(CommitHook); ok {
				return h, true
			}
		}
	}
	if dbGet != nil {
		if v, ok := dbGet(CommitHookKey); ok {
			if h, ok := v.(CommitHook); ok {
				return h, true
			}
		}
	}
	return nil, false
}
