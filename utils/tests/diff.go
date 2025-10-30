package tests

import (
	"reflect"
	"strings"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// isEmptyJSON checks if a JSON string represents an empty object or array
func isEmptyJSON(jsonStr string) bool {
	trimmed := strings.TrimSpace(jsonStr)
	return trimmed == "{}" || trimmed == "[]" || trimmed == "null"
}

// Diff compares this TestDBConfig instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestDBConfig) Diff(old *TestDBConfig) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare LogLevel

	// Comparable type comparison
	if new.LogLevel != old.LogLevel {
		diff["LogLevel"] = new.LogLevel
	}

	// Compare DSN

	// Simple type comparison
	if new.DSN != old.DSN {
		diff["DSN"] = new.DSN
	}

	return diff
}

// Diff compares this TestUserBuilder instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestUserBuilder) Diff(old *TestUserBuilder) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare user

	// Pointer to struct comparison
	if new.user == nil || old.user == nil {
		if new.user != old.user {

			diff["user"] = new.user

		}
	} else {
		nestedDiff := new.user.Diff(old.user)
		if len(nestedDiff) > 0 {

			diff["user"] = nestedDiff

		}
	}

	return diff
}

// Diff compares this TestProfileBuilder instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestProfileBuilder) Diff(old *TestProfileBuilder) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare profile

	// Pointer to struct comparison
	if new.profile == nil || old.profile == nil {
		if new.profile != old.profile {

			diff["profile"] = new.profile

		}
	} else {
		nestedDiff := new.profile.Diff(old.profile)
		if len(nestedDiff) > 0 {

			diff["profile"] = nestedDiff

		}
	}

	return diff
}

// Diff compares this TestPostBuilder instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestPostBuilder) Diff(old *TestPostBuilder) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare post

	// Pointer to struct comparison
	if new.post == nil || old.post == nil {
		if new.post != old.post {

			diff["post"] = new.post

		}
	} else {
		nestedDiff := new.post.Diff(old.post)
		if len(nestedDiff) > 0 {

			diff["post"] = nestedDiff

		}
	}

	return diff
}

// Diff compares this UserData instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *UserData) Diff(old *UserData) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Day

	// Simple type comparison
	if new.Day != old.Day {
		diff["day"] = new.Day
	}

	// Compare Nickname

	// Simple type comparison
	if new.Nickname != old.Nickname {
		diff["nickname"] = new.Nickname
	}

	// Compare Married

	// Simple type comparison
	if new.Married != old.Married {
		diff["married"] = new.Married
	}

	return diff
}

// Diff compares this WhatsAppStatus instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *WhatsAppStatus) Diff(old *WhatsAppStatus) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Mode

	// Simple type comparison
	if new.Mode != old.Mode {
		diff["mode"] = new.Mode
	}

	// Compare State

	// Simple type comparison
	if new.State != old.State {
		diff["state"] = new.State
	}

	// Compare IsStarted

	// Simple type comparison
	if new.IsStarted != old.IsStarted {
		diff["isStarted"] = new.IsStarted
	}

	// Compare WaVersion

	// Simple type comparison
	if new.WaVersion != old.WaVersion {
		diff["waVersion"] = new.WaVersion
	}

	// Compare IsOnQrPage

	// Simple type comparison
	if new.IsOnQrPage != old.IsOnQrPage {
		diff["isOnQrPage"] = new.IsOnQrPage
	}

	// Compare IsWebConnected

	// Simple type comparison
	if new.IsWebConnected != old.IsWebConnected {
		diff["isWebConnected"] = new.IsWebConnected
	}

	// Compare QrCodeExpiresAt

	// Simple type comparison
	if new.QrCodeExpiresAt != old.QrCodeExpiresAt {
		diff["qrCodeExpiresAt"] = new.QrCodeExpiresAt
	}

	// Compare QrCodeUrl

	// Simple type comparison
	if new.QrCodeUrl != old.QrCodeUrl {
		diff["qrCodeUrl"] = new.QrCodeUrl
	}

	return diff
}

// Diff compares this WhatsAppData instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *WhatsAppData) Diff(old *WhatsAppData) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Error

	// Simple type comparison
	if new.Error != old.Error {
		diff["error"] = new.Error
	}

	// Compare Status

	// Pointer to struct comparison
	if new.Status == nil || old.Status == nil {
		if new.Status != old.Status {

			// For JSONB nested struct, setting to nil should set the entire field to null
			diff["status"] = new.Status

		}
	} else {
		nestedDiff := new.Status.Diff(old.Status)
		if len(nestedDiff) > 0 {

			// Flatten nested JSONB struct diffs with dot notation for proper jsonb_set paths
			for key, value := range nestedDiff {
				diff["status."+key] = value
			}

		}
	}

	// Compare DriverId

	// Simple type comparison
	if new.DriverId != old.DriverId {
		diff["driverId"] = new.DriverId
	}

	return diff
}

// Diff compares this TestUser instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestUser) Diff(old *TestUser) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Id

	// UUID comparison

	// Direct UUID comparison
	if new.Id != old.Id {
		diff["id"] = new.Id
	}

	// Compare Name

	// Simple type comparison
	if new.Name != old.Name {
		diff["name"] = new.Name
	}

	// Compare Email

	// Simple type comparison
	if new.Email != old.Email {
		diff["email"] = new.Email
	}

	// Compare Age

	// Simple type comparison
	if new.Age != old.Age {
		diff["age"] = new.Age
	}

	// Compare Active

	// Simple type comparison
	if new.Active != old.Active {
		diff["active"] = new.Active
	}

	// Compare ArchivedAt

	// Time comparison

	// Pointer to time comparison
	if (new.ArchivedAt == nil) != (old.ArchivedAt == nil) || (new.ArchivedAt != nil && !new.ArchivedAt.Equal(*old.ArchivedAt)) {
		diff["archivedAt"] = new.ArchivedAt
	}

	// Compare Profile

	// Pointer to struct comparison
	if new.Profile == nil || old.Profile == nil {
		if new.Profile != old.Profile {

			// For JSONB nested struct, setting to nil should set the entire field to null
			diff["profile"] = new.Profile

		}
	} else {
		nestedDiff := new.Profile.Diff(old.Profile)
		if len(nestedDiff) > 0 {

			// Flatten nested JSONB struct diffs with dot notation for proper jsonb_set paths
			for key, value := range nestedDiff {
				diff["profile."+key] = value
			}

		}
	}

	// Compare Posts

	// Complex type comparison (slice, map, interface, etc.)
	if !reflect.DeepEqual(new.Posts, old.Posts) {
		diff["posts"] = new.Posts
	}

	// Compare Data

	// JSON field comparison - handle both datatypes.JSON and struct types with jsonb storage

	// JSON field comparison - attribute-by-attribute diff for struct types

	// Handle pointer to struct
	if new.Data == nil && old.Data != nil {
		// new is nil, old is not nil - set to null
		diff["data"] = nil
	} else if new.Data != nil && old.Data == nil {
		// new is not nil, old is nil - use entire new
		jsonValue, err := sonic.Marshal(new.Data)
		if err == nil && !isEmptyJSON(string(jsonValue)) {
			diff["data"] = gorm.Expr("? || ?", clause.Column{Name: "data"}, string(jsonValue))
		} else if err != nil {
			diff["data"] = new.Data
		}
	} else if new.Data != nil && old.Data != nil {
		// Both are not nil - use attribute-by-attribute diff
		DataDiff := new.Data.Diff(old.Data)
		if len(DataDiff) > 0 {
			// Check if the diff contains flattened paths (dot notation)
			hasFlattenedPaths := false
			for key := range DataDiff {
				if strings.Contains(key, ".") {
					hasFlattenedPaths = true
					break
				}
			}

			if hasFlattenedPaths {
				// Flatten paths at this level too: whatsAppData.status.mode
				for key, value := range DataDiff {
					diff["data."+key] = value
				}
			} else {
				// No flattened paths - use traditional || merge
				jsonValue, err := sonic.Marshal(DataDiff)
				if err == nil && !isEmptyJSON(string(jsonValue)) {
					diff["data"] = gorm.Expr("? || ?", clause.Column{Name: "data"}, string(jsonValue))
				} else if err != nil {
					// Fallback to regular assignment if JSON marshaling fails
					diff["data"] = new.Data
				}
			}
		}
	}

	// Compare WhatsAppData

	// JSON field comparison - handle both datatypes.JSON and struct types with jsonb storage

	// JSON field comparison - attribute-by-attribute diff for struct types

	// Handle pointer to struct
	if new.WhatsAppData == nil && old.WhatsAppData != nil {
		// new is nil, old is not nil - set to null
		diff["whatsAppData"] = nil
	} else if new.WhatsAppData != nil && old.WhatsAppData == nil {
		// new is not nil, old is nil - use entire new
		jsonValue, err := sonic.Marshal(new.WhatsAppData)
		if err == nil && !isEmptyJSON(string(jsonValue)) {
			diff["whatsAppData"] = gorm.Expr("? || ?", clause.Column{Name: "whats_app_data"}, string(jsonValue))
		} else if err != nil {
			diff["whatsAppData"] = new.WhatsAppData
		}
	} else if new.WhatsAppData != nil && old.WhatsAppData != nil {
		// Both are not nil - use attribute-by-attribute diff
		WhatsAppDataDiff := new.WhatsAppData.Diff(old.WhatsAppData)
		if len(WhatsAppDataDiff) > 0 {
			// Check if the diff contains flattened paths (dot notation)
			hasFlattenedPaths := false
			for key := range WhatsAppDataDiff {
				if strings.Contains(key, ".") {
					hasFlattenedPaths = true
					break
				}
			}

			if hasFlattenedPaths {
				// Flatten paths at this level too: whatsAppData.status.mode
				for key, value := range WhatsAppDataDiff {
					diff["whatsAppData."+key] = value
				}
			} else {
				// No flattened paths - use traditional || merge
				jsonValue, err := sonic.Marshal(WhatsAppDataDiff)
				if err == nil && !isEmptyJSON(string(jsonValue)) {
					diff["whatsAppData"] = gorm.Expr("? || ?", clause.Column{Name: "whats_app_data"}, string(jsonValue))
				} else if err != nil {
					// Fallback to regular assignment if JSON marshaling fails
					diff["whatsAppData"] = new.WhatsAppData
				}
			}
		}
	}

	return diff
}

// Diff compares this TestProfile instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestProfile) Diff(old *TestProfile) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Id

	// UUID comparison

	// Direct UUID comparison
	if new.Id != old.Id {
		diff["id"] = new.Id
	}

	// Compare UserId

	// UUID comparison

	// Direct UUID comparison
	if new.UserId != old.UserId {
		diff["userId"] = new.UserId
	}

	// Compare Bio

	// Simple type comparison
	if new.Bio != old.Bio {
		diff["bio"] = new.Bio
	}

	// Compare Website

	// Simple type comparison
	if new.Website != old.Website {
		diff["website"] = new.Website
	}

	// Compare Settings

	// Simple type comparison
	if new.Settings != old.Settings {
		diff["settings"] = new.Settings
	}

	return diff
}

// Diff compares this TestPost instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestPost) Diff(old *TestPost) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Id

	// UUID comparison

	// Direct UUID comparison
	if new.Id != old.Id {
		diff["id"] = new.Id
	}

	// Compare UserId

	// UUID comparison

	// Direct UUID comparison
	if new.UserId != old.UserId {
		diff["userId"] = new.UserId
	}

	// Compare Title

	// Simple type comparison
	if new.Title != old.Title {
		diff["title"] = new.Title
	}

	// Compare Content

	// Simple type comparison
	if new.Content != old.Content {
		diff["content"] = new.Content
	}

	// Compare Published

	// Simple type comparison
	if new.Published != old.Published {
		diff["published"] = new.Published
	}

	// Compare Tags

	// Complex type comparison (slice, map, interface, etc.)
	if !reflect.DeepEqual(new.Tags, old.Tags) {
		diff["tags"] = new.Tags
	}

	// Compare CreatedAt

	// Time comparison

	// Direct time comparison
	if !new.CreatedAt.Equal(old.CreatedAt) {
		diff["createdAt"] = new.CreatedAt

	}

	// Compare UpdatedAt

	// Time comparison

	// Direct time comparison
	if !new.UpdatedAt.Equal(old.UpdatedAt) {
		diff["updatedAt"] = new.UpdatedAt

	}

	return diff
}

// Diff compares this TestTag instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestTag) Diff(old *TestTag) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Id

	// UUID comparison

	// Direct UUID comparison
	if new.Id != old.Id {
		diff["id"] = new.Id
	}

	// Compare Name

	// Simple type comparison
	if new.Name != old.Name {
		diff["name"] = new.Name
	}

	// Compare Posts

	// Complex type comparison (slice, map, interface, etc.)
	if !reflect.DeepEqual(new.Posts, old.Posts) {
		diff["posts"] = new.Posts
	}

	return diff
}

// Diff compares this TestSimpleEntity instance (new) with another (old) and returns a map of differences
// with only the new values for fields that have changed.
// Usage: newValues = new.Diff(old)
// Returns nil if either pointer is nil.
func (new *TestSimpleEntity) Diff(old *TestSimpleEntity) map[string]interface{} {
	// Handle nil pointers
	if new == nil || old == nil {
		return nil
	}

	diff := make(map[string]interface{})

	// Compare Id

	// UUID comparison

	// Direct UUID comparison
	if new.Id != old.Id {
		diff["id"] = new.Id
	}

	// Compare Value

	// Simple type comparison
	if new.Value != old.Value {
		diff["value"] = new.Value
	}

	return diff
}
