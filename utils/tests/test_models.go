package tests

import (
	"time"

	"github.com/google/uuid"
)

// TestUser represents a test entity for repository testing
type TestUser struct {
	ID        uuid.UUID    `gorm:"type:text;primary_key" json:"id"`
	Name      string       `gorm:"not null" json:"name"`
	Email     string       `gorm:"unique;not null" json:"email"`
	Age       int          `json:"age"`
	Active    bool         `json:"active"`
	Profile   *TestProfile `gorm:"foreignKey:UserID" json:"profile,omitempty"`
	Posts     []TestPost   `gorm:"foreignKey:UserID" json:"posts,omitempty"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// Clone implements the Diffable interface for pointer types
func (u *TestUser) Clone() *TestUser {
	if u == nil {
		return nil
	}
	clone := *u
	if u.Profile != nil {
		profileClone := *u.Profile
		clone.Profile = &profileClone
	}
	if u.Posts != nil {
		clone.Posts = make([]TestPost, len(u.Posts))
		copy(clone.Posts, u.Posts)
	}
	return &clone
}

// Diff implements the Diffable interface for pointer types
func (u *TestUser) Diff(other *TestUser) map[string]interface{} {
	if u == nil || other == nil {
		return make(map[string]interface{})
	}

	diff := make(map[string]interface{})

	if u.Name != other.Name {
		diff["name"] = u.Name
	}
	if u.Email != other.Email {
		diff["email"] = u.Email
	}
	if u.Age != other.Age {
		diff["age"] = u.Age
	}
	if u.Active != other.Active {
		diff["active"] = u.Active
	}

	return diff
}

// TestProfile represents a user profile for testing relationships
type TestProfile struct {
	ID       uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	UserID   uuid.UUID `gorm:"type:text;not null" json:"userId"`
	Bio      string    `json:"bio"`
	Website  string    `json:"website"`
	Settings string    `gorm:"type:text" json:"settings"`
}

// TestPost represents a blog post for testing one-to-many relationships
type TestPost struct {
	ID        uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	UserID    uuid.UUID `gorm:"type:text;not null" json:"userId"`
	Title     string    `gorm:"not null" json:"title"`
	Content   string    `json:"content"`
	Published bool      `json:"published"`
	Tags      []TestTag `gorm:"many2many:post_tags;" json:"tags,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TestTag represents a tag for testing many-to-many relationships
type TestTag struct {
	ID    uuid.UUID  `gorm:"type:text;primary_key" json:"id"`
	Name  string     `gorm:"unique;not null" json:"name"`
	Posts []TestPost `gorm:"many2many:post_tags;" json:"posts,omitempty"`
}

// TestSimpleEntity represents a simple entity without relationships
type TestSimpleEntity struct {
	ID    uuid.UUID `gorm:"type:text;primary_key" json:"id"`
	Value string    `json:"value"`
}

// Clone implements the Diffable interface for pointer types
func (e *TestSimpleEntity) Clone() *TestSimpleEntity {
	if e == nil {
		return nil
	}
	clone := *e
	return &clone
}

// Diff implements the Diffable interface for pointer types
func (e *TestSimpleEntity) Diff(other *TestSimpleEntity) map[string]interface{} {
	if e == nil || other == nil {
		return make(map[string]interface{})
	}

	diff := make(map[string]interface{})
	if e.Value != other.Value {
		diff["value"] = e.Value
	}
	return diff
}
