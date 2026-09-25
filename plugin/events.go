package plugin

import (
	"context"
	"time"
)

// EventKind names a cache decision. The names match the Node.js
// ResourceCache debug log ("[cache] <kind> key=..."), so logs from both
// backends read the same way.
type EventKind string

const (
	EventHit        EventKind = "hit"
	EventMiss       EventKind = "miss"
	EventSet        EventKind = "set"
	EventSkip       EventKind = "skip"
	EventInvalidate EventKind = "invalidate"
	EventError      EventKind = "error"
)

// Event is one cache decision. SQL is set on misses; Reason on skips,
// errors and invalidations (create/update/delete).
type Event struct {
	Kind   EventKind
	Model  string
	Key    string
	Reason string
	Tags   []string
	SQL    string
	Err    error
}

// EventRecorder receives every cache decision, e.g. to log it.
type EventRecorder interface {
	Record(ctx context.Context, e Event)
}

// WithEventRecorder enables per-decision events. Nil (the default) disables them.
func WithEventRecorder(r EventRecorder) Option {
	return func(o *options) { o.recorder = r }
}

func (p *Plugin) emit(ctx context.Context, e Event) {
	if p.options.recorder != nil {
		p.options.recorder.Record(ctx, e)
	}
}

func (p *Plugin) set(ctx context.Context, key, model string, data []byte, tags []string, ttl time.Duration) error {
	if err := p.cache.Set(ctx, key, data, tags, ttl); err != nil {
		p.emit(ctx, Event{Kind: EventError, Model: model, Key: key, Reason: "set", Err: err})
		return err
	}
	p.emit(ctx, Event{Kind: EventSet, Model: model, Key: key, Tags: tags})
	return nil
}

func (p *Plugin) invalidate(ctx context.Context, model, reason string, tags []string) error {
	if err := p.cache.Invalidate(ctx, tags); err != nil {
		p.emit(ctx, Event{Kind: EventError, Model: model, Reason: "invalidate", Tags: tags, Err: err})
		return err
	}
	p.emit(ctx, Event{Kind: EventInvalidate, Model: model, Reason: reason, Tags: tags})
	return nil
}
