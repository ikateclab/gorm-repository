package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
)

func ttlStmt(table string) *gorm.DB {
	return &gorm.DB{Statement: &gorm.Statement{Table: table}}
}

func TestResolveTTL_FixedWhenNoFuncSet(t *testing.T) {
	p := New(nil, WithDefaultTTL(5*time.Minute))
	assert.Equal(t, 5*time.Minute, p.resolveTTL(ttlStmt("whatever")))
}

func TestResolveTTL_FuncTakesPrecedenceOverFixed(t *testing.T) {
	calls := 0
	p := New(nil, WithDefaultTTL(time.Minute), WithDefaultTTLFunc(func() time.Duration {
		calls++
		return 42 * time.Second
	}))

	got := p.resolveTTL(ttlStmt("whatever"))

	assert.Equal(t, 42*time.Second, got)
	assert.Equal(t, 1, calls, "should be invoked exactly once per resolveTTL call")
}

func TestResolveTTL_PerModelOverrideBeatsFunc(t *testing.T) {
	p := New(nil, WithDefaultTTLFunc(func() time.Duration { return time.Hour }))
	p.Register("users", ModelOptions{TTL: 30 * time.Second})

	assert.Equal(t, 30*time.Second, p.resolveTTL(ttlStmt("users")))
}
