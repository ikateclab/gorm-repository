package plugin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ikateclab/gorm-repository/plugin/memory"
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

// RegisterModel only knows GORM's default naming at construction time;
// once installed, the registration must follow the DB's own namer, or it
// silently never matches Statement.Table.
func TestRegisterModel_ResolvedWithDBNamingStrategy(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	require.NoError(t, err)

	p := New(memory.New(),
		RegisterModel[regAuditLog](ModelWithTTL(42*time.Second)),
		RegisterModel[regUser](ModelWithBypass()),
	)
	require.NoError(t, db.Use(p))

	mo, ok := p.LookupModel("reg_audit_log")
	require.True(t, ok, "registration must be keyed by the DB namer's table")
	assert.Equal(t, 42*time.Second, mo.TTL)
	_, ok = p.LookupModel("reg_audit_logs")
	assert.False(t, ok)

	// TableName() wins over any namer, so this one never moves.
	mo, ok = p.LookupModel("reg_users")
	require.True(t, ok)
	assert.True(t, mo.Bypass)

	require.NoError(t, db.AutoMigrate(&regAuditLog{}))
	var logs []regAuditLog
	res := db.Find(&logs)
	require.NoError(t, res.Error)
	assert.Equal(t, "reg_audit_log", res.Statement.Table)
	assert.Equal(t, 42*time.Second, p.resolveTTL(res), "the per-model TTL must apply to real queries")
}
