package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fakeHook is a minimal CommitHook for tests.
type fakeHook struct {
	commits   []func(context.Context) error
	rollbacks []func()
}

func (f *fakeHook) OnCommit(fn func(context.Context) error) {
	f.commits = append(f.commits, fn)
}

func (f *fakeHook) OnRollback(fn func()) {
	f.rollbacks = append(f.rollbacks, fn)
}

func TestLookupCommitHook_FromContext(t *testing.T) {
	hook := &fakeHook{}
	ctx := CommitHookContext(context.Background(), hook)

	got, ok := LookupCommitHook(ctx, nil)
	assert.True(t, ok)
	assert.Same(t, hook, got)
}

func TestLookupCommitHook_FromSettings(t *testing.T) {
	hook := &fakeHook{}
	settings := func(k string) (interface{}, bool) {
		if k == CommitHookKey {
			return CommitHook(hook), true
		}
		return nil, false
	}

	got, ok := LookupCommitHook(nil, settings)
	assert.True(t, ok)
	assert.Same(t, hook, got)
}

func TestLookupCommitHook_NotFound(t *testing.T) {
	got, ok := LookupCommitHook(context.Background(), nil)
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestLookupCommitHook_WrongType(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxCommitHookKey, "not-a-hook")
	got, ok := LookupCommitHook(ctx, nil)
	assert.False(t, ok)
	assert.Nil(t, got)
}
