package plugin

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestIDHint_RoundTrip(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}

	_, ok := IDHint(db)
	assert.False(t, ok, "no hint set yet")

	hinted := WithIDHint(db, "abc-123")
	id, ok := IDHint(hinted)
	assert.True(t, ok)
	assert.Equal(t, "abc-123", id)

	// The original session must be unaffected — WithIDHint returns a new
	// session (via db.Set), it doesn't mutate the caller's db in place.
	_, ok = IDHint(db)
	assert.False(t, ok)
}

func TestIDHint_EmptyIDNotConsideredSet(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	hinted := WithIDHint(db, "")
	_, ok := IDHint(hinted)
	assert.False(t, ok)
}
