package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type regUser struct {
	ID   uint
	Name string
}

func (regUser) TableName() string { return "reg_users" }

type regAuditLog struct {
	ID uint
}

func TestRegisterModel_TTL(t *testing.T) {
	p := New(nil,
		RegisterModel[regUser](ModelWithTTL(30*time.Second)),
	)
	mo, ok := p.LookupModel("reg_users")
	require.True(t, ok)
	assert.Equal(t, 30*time.Second, mo.TTL)
}

func TestRegisterModel_Bypass(t *testing.T) {
	p := New(nil,
		RegisterModel[regAuditLog](ModelWithBypass()),
	)
	// regAuditLog has no TableName; falls back to naming strategy.
	mo, ok := p.LookupModel("reg_audit_logs")
	require.True(t, ok)
	assert.True(t, mo.Bypass)
}

func TestRegisterModel_ExtraTags(t *testing.T) {
	p := New(nil,
		RegisterModel[regUser](ModelWithExtraTags("hot", "featured")),
	)
	mo, ok := p.LookupModel("reg_users")
	require.True(t, ok)
	assert.Equal(t, []string{"hot", "featured"}, mo.ExtraTags)
}

func TestRegisterModel_Multiple(t *testing.T) {
	p := New(nil,
		RegisterModel[regUser](ModelWithTTL(time.Minute)),
		RegisterModel[regAuditLog](ModelWithBypass()),
	)
	_, ok := p.LookupModel("reg_users")
	assert.True(t, ok)
	_, ok = p.LookupModel("reg_audit_logs")
	assert.True(t, ok)
}
