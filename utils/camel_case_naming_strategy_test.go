package utils

import (
	"testing"

	"gorm.io/gorm/schema"
)

func TestCamelCaseNamingStrategy_TableName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		input    string
		expected string
	}{
		{"users", "users"},
		{"user_profiles", "user_profiles"},
		{"UserProfiles", "UserProfiles"},
		{"", ""},
	}

	for _, test := range tests {
		result := strategy.TableName(test.input)
		if result != test.expected {
			t.Errorf("TableName(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_SchemaName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		input    string
		expected string
	}{
		{"public", "public"},
		{"test_schema", "test_schema"},
		{"", ""},
	}

	for _, test := range tests {
		result := strategy.SchemaName(test.input)
		if result != test.expected {
			t.Errorf("SchemaName(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_ColumnName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		table    string
		column   string
		expected string
	}{
		{"users", "FirstName", "firstName"},
		{"users", "LastName", "lastName"},
		{"users", "Id", "id"},
		{"users", "CreatedAt", "createdAt"},
		{"users", "UpdatedAt", "updatedAt"},
		{"users", "UserId", "userId"},
		{"users", "XMLData", "xmldata"},
		{"users", "HTTPStatus", "httpstatus"},
		{"users", "name", "name"},
		{"users", "email", "email"},
		{"users", "", ""},
	}

	for _, test := range tests {
		result := strategy.ColumnName(test.table, test.column)
		if result != test.expected {
			t.Errorf("ColumnName(%s, %s) = %s, expected %s", test.table, test.column, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_JoinTableName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		input    string
		expected string
	}{
		{"user_roles", "user_roles"},
		{"post_tags", "post_tags"},
		{"", ""},
	}

	for _, test := range tests {
		result := strategy.JoinTableName(test.input)
		if result != test.expected {
			t.Errorf("JoinTableName(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_RelationshipFKName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	// Create a mock relationship
	rel := schema.Relationship{
		Name: "User",
		Field: &schema.Field{
			Name: "Id",
		},
	}

	expected := "user_id_fkey"
	result := strategy.RelationshipFKName(rel)
	if result != expected {
		t.Errorf("RelationshipFKName() = %s, expected %s", result, expected)
	}
}

func TestCamelCaseNamingStrategy_CheckerName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		table    string
		column   string
		expected string
	}{
		{"users", "age", "chk_users_age"},
		{"posts", "status", "chk_posts_status"},
		{"", "", "chk__"},
	}

	for _, test := range tests {
		result := strategy.CheckerName(test.table, test.column)
		if result != test.expected {
			t.Errorf("CheckerName(%s, %s) = %s, expected %s", test.table, test.column, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_IndexName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		table    string
		column   string
		expected string
	}{
		{"users", "email", "idx_users_email"},
		{"posts", "created_at", "idx_posts_created_at"},
		{"", "", "idx__"},
	}

	for _, test := range tests {
		result := strategy.IndexName(test.table, test.column)
		if result != test.expected {
			t.Errorf("IndexName(%s, %s) = %s, expected %s", test.table, test.column, result, test.expected)
		}
	}
}

func TestCamelCaseNamingStrategy_UniqueName(t *testing.T) {
	strategy := CamelCaseNamingStrategy{}

	tests := []struct {
		table    string
		column   string
		expected string
	}{
		{"users", "email", "uq_users_email"},
		{"posts", "slug", "uq_posts_slug"},
		{"", "", "uq__"},
	}

	for _, test := range tests {
		result := strategy.UniqueName(test.table, test.column)
		if result != test.expected {
			t.Errorf("UniqueName(%s, %s) = %s, expected %s", test.table, test.column, result, test.expected)
		}
	}
}

func TestToLowerCamelCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"FirstName", "firstName"},
		{"LastName", "lastName"},
		{"Id", "id"},
		{"XMLData", "xmldata"},
		{"HTTPStatus", "httpstatus"},
		{"CreatedAt", "createdAt"},
		{"UpdatedAt", "updatedAt"},
		{"UserId", "userId"},
		{"name", "name"},
		{"email", "email"},
		{"", ""},
		{"A", "a"},
		{"AB", "ab"},
		{"ABC", "abc"},
		{"AbC", "abC"},
	}

	for _, test := range tests {
		result := toLowerCamelCase(test.input)
		if result != test.expected {
			t.Errorf("toLowerCamelCase(%s) = %s, expected %s", test.input, result, test.expected)
		}
	}
}
