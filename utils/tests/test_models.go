package tests

import (
	"time"

	"github.com/google/uuid"
)

/// go :  go run ../../../gorm-tracked-updates/cmd/gorm-gen/main.go -package=.
//go:generate go run github.com/ikateclab/gorm-tracked-updates/cmd/gorm-gen@v0.0.5 -package=.

// @jsonb
type UserData struct {
	Day      int    `json:"day,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Married  bool   `json:"married,omitempty"`
}

// @jsonb
type WhatsAppStatus struct {
	Mode            string `json:"mode,omitempty"`
	State           string `json:"state,omitempty"`
	IsStarted       bool   `json:"isStarted,omitempty"`
	WaVersion       string `json:"waVersion,omitempty"`
	IsOnQrPage      bool   `json:"isOnQrPage,omitempty"`
	IsWebConnected  bool   `json:"isWebConnected,omitempty"`
	QrCodeExpiresAt string `json:"qrCodeExpiresAt,omitempty"`
	QrCodeUrl       string `json:"qrCodeUrl,omitempty"`
}

// @jsonb
type WhatsAppData struct {
	Error    string          `json:"error,omitempty"`
	Status   *WhatsAppStatus `json:"status,omitempty"`
	DriverId string          `json:"driverId,omitempty"`
}

// TestUser represents a test entity for repository testing
// @jsonb
type TestUser struct {
	Id           uuid.UUID     `gorm:"type:text;primary_key" json:"id"`
	Name         string        `gorm:"not null" json:"name"`
	Email        string        `gorm:"unique;not null" json:"email"`
	Age          int           `json:"age"`
	Active       bool          `json:"active"`
	ArchivedAt   *time.Time    `gorm:"column:archivedAt;type:timestamptz" json:"archivedAt,omitempty"`
	Profile      *TestProfile  `gorm:"foreignKey:UserId" json:"profile,omitempty"`
	Posts        []*TestPost   `gorm:"foreignKey:UserId" json:"posts,omitempty"`
	Data         *UserData     `gorm:"type:jsonb;serializer:json;not null;default:'{}'" json:"data,omitempty"`
	WhatsAppData *WhatsAppData `gorm:"type:jsonb;serializer:json" json:"whatsAppData,omitempty"`
}

// TestProfile represents a user profile for testing relationships
// @jsonb
type TestProfile struct {
	Id       uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	UserId   uuid.UUID `gorm:"type:text;not null" json:"userId"`
	Bio      string    `json:"bio"`
	Website  string    `json:"website"`
	Settings string    `gorm:"type:text" json:"settings"`
}

// TestPost represents a blog post for testing one-to-many relationships
// @jsonb
type TestPost struct {
	Id        uuid.UUID  `gorm:"type:text;primary_key" json:"id"`
	UserId    uuid.UUID  `gorm:"type:text;not null" json:"userId"`
	Title     string     `gorm:"not null" json:"title"`
	Content   string     `json:"content"`
	Published bool       `json:"published"`
	Tags      []*TestTag `gorm:"many2many:post_tags;" json:"tags,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

// TestTag represents a tag for testing many-to-many relationships
// @jsonb
type TestTag struct {
	Id    uuid.UUID   `gorm:"type:text;primary_key" json:"id"`
	Name  string      `gorm:"unique;not null" json:"name"`
	Posts []*TestPost `gorm:"many2many:post_tags;" json:"posts,omitempty"`
}

// TestSimpleEntity represents a simple entity without relationships

// @jsonb
type TestSimpleEntity struct {
	Id    uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	Value string    `json:"value"`
}
