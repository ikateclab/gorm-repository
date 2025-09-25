package utils

import (
	"reflect"
	"testing"
)

// Test entity for entity_to_map tests
type TestEntity struct {
	Id       int                    `json:"id"`
	Name     string                 `json:"name"`
	Email    string                 `json:"email"`
	Age      int                    `json:"age"`
	Active   bool                   `json:"active"`
	Settings map[string]interface{} `json:"settings"`
	Profile  *TestProfile           `json:"profile"`
}

type TestProfile struct {
	Bio     string `json:"bio"`
	Website string `json:"website"`
}

func TestEntityToMap_SimpleFields(t *testing.T) {
	entity := TestEntity{
		Id:     1,
		Name:   "John Doe",
		Email:  "john@example.com",
		Age:    30,
		Active: true,
	}

	fields := map[string]interface{}{
		"Name":   nil,
		"Email":  nil,
		"Age":    nil,
		"Active": nil,
	}

	result, err := EntityToMap(fields, entity)
	if err != nil {
		t.Errorf("EntityToMap failed: %v", err)
	}

	expectedFields := []string{"name", "email", "age", "active"}
	for _, field := range expectedFields {
		if _, exists := result[field]; !exists {
			t.Errorf("Expected field %s not found in result", field)
		}
	}

	if result["name"] != "John Doe" {
		t.Errorf("Expected name 'John Doe', got %v", result["name"])
	}
	if result["email"] != "john@example.com" {
		t.Errorf("Expected email 'john@example.com', got %v", result["email"])
	}
	if result["age"] != 30 {
		t.Errorf("Expected age 30, got %v", result["age"])
	}
	if result["active"] != true {
		t.Errorf("Expected active true, got %v", result["active"])
	}
}

func TestEntityToMap_NonExistentField(t *testing.T) {
	entity := TestEntity{
		Id:   1,
		Name: "John Doe",
	}

	fields := map[string]interface{}{
		"NonExistentField": nil,
	}

	_, err := EntityToMap(fields, entity)
	if err == nil {
		t.Error("Expected error for non-existent field, but got nil")
	}

	expectedError := "field not found in entity: NonExistentField"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}

func TestEntityToMap_PointerField(t *testing.T) {
	profile := &TestProfile{
		Bio:     "Test bio",
		Website: "https://example.com",
	}

	entity := TestEntity{
		Id:      1,
		Name:    "John Doe",
		Profile: profile,
	}

	fields := map[string]interface{}{
		"Profile": map[string]interface{}{
			"Bio":     nil,
			"Website": nil,
		},
	}

	result, err := EntityToMap(fields, entity)
	if err != nil {
		t.Errorf("EntityToMap failed: %v", err)
	}

	if _, exists := result["profile"]; !exists {
		t.Error("Expected profile field not found in result")
	}
}

func TestEntityToMap_NilPointerField(t *testing.T) {
	entity := TestEntity{
		Id:      1,
		Name:    "John Doe",
		Profile: nil,
	}

	fields := map[string]interface{}{
		"Profile": map[string]interface{}{
			"Bio": nil,
		},
	}

	_, err := EntityToMap(fields, entity)
	if err == nil {
		t.Error("Expected error for nil pointer field, but got nil")
	}

	expectedError := "nil pointer encountered for field: Profile"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}

func TestEntityToMap_SmallFieldCount(t *testing.T) {
	entity := TestEntity{
		Id:   1,
		Name: "John Doe",
		Age:  30,
	}

	// Test with exactly smallFieldCount (4) fields
	fields := map[string]interface{}{
		"Id":   nil,
		"Name": nil,
		"Age":  nil,
	}

	result, err := EntityToMap(fields, entity)
	if err != nil {
		t.Errorf("EntityToMap failed: %v", err)
	}

	if len(result) != 3 {
		t.Errorf("Expected 3 fields in result, got %d", len(result))
	}
}

func TestGetFieldInfoMap_Caching(t *testing.T) {
	entityType := reflect.TypeOf(TestEntity{})

	// First call should create the mapping
	fieldMap1 := getFieldInfoMap(entityType)
	if len(fieldMap1) == 0 {
		t.Error("Expected non-empty field map")
	}

	// Second call should return cached mapping
	fieldMap2 := getFieldInfoMap(entityType)
	if len(fieldMap2) != len(fieldMap1) {
		t.Error("Cached field map should have same length as original")
	}

	// Verify specific fields exist
	expectedFields := []string{"Id", "Name", "Email", "Age", "Active", "Settings", "Profile"}
	for _, field := range expectedFields {
		if _, exists := fieldMap1[field]; !exists {
			t.Errorf("Expected field %s not found in field map", field)
		}
	}
}

func TestGetJSONName(t *testing.T) {
	tests := []struct {
		fieldName string
		jsonTag   string
		expected  string
	}{
		{"Name", "name", "name"},
		{"Email", "email,omitempty", "email"},
		{"Age", "", "Age"},
		{"Active", "-", "Active"},
		{"Settings", "settings,omitempty,json", "settings"},
	}

	for _, test := range tests {
		field := reflect.StructField{
			Name: test.fieldName,
			Tag:  reflect.StructTag(`json:"` + test.jsonTag + `"`),
		}

		result := getJSONName(field)
		if result != test.expected {
			t.Errorf("getJSONName for field %s with tag %s = %s, expected %s",
				test.fieldName, test.jsonTag, result, test.expected)
		}
	}
}

func TestHandleNestedFields_Struct(t *testing.T) {
	profile := TestProfile{
		Bio:     "Test bio",
		Website: "https://example.com",
	}

	fieldValue := reflect.ValueOf(profile)
	subMap := map[string]interface{}{
		"Bio":     nil,
		"Website": nil,
	}

	result, err := handleNestedFields(fieldValue, subMap)
	if err != nil {
		t.Errorf("handleNestedFields failed: %v", err)
	}

	if result["bio"] != "Test bio" {
		t.Errorf("Expected bio 'Test bio', got %v", result["bio"])
	}
	if result["website"] != "https://example.com" {
		t.Errorf("Expected website 'https://example.com', got %v", result["website"])
	}
}

func TestHandleNestedFields_Map(t *testing.T) {
	settings := map[string]interface{}{
		"theme":    "dark",
		"language": "en",
	}

	fieldValue := reflect.ValueOf(settings)
	subMap := map[string]interface{}{
		"theme":    nil,
		"language": nil,
	}

	result, err := handleNestedFields(fieldValue, subMap)
	if err != nil {
		t.Errorf("handleNestedFields failed: %v", err)
	}

	if result["theme"] != "dark" {
		t.Errorf("Expected theme 'dark', got %v", result["theme"])
	}
	if result["language"] != "en" {
		t.Errorf("Expected language 'en', got %v", result["language"])
	}
}

func TestHandleNestedFields_UnsupportedType(t *testing.T) {
	fieldValue := reflect.ValueOf(42) // int is not supported for nested fields
	subMap := map[string]interface{}{
		"test": nil,
	}

	_, err := handleNestedFields(fieldValue, subMap)
	if err == nil {
		t.Error("Expected error for unsupported type, but got nil")
	}

	expectedError := "unsupported type for nested fields"
	if err.Error() != expectedError {
		t.Errorf("Expected error '%s', got '%s'", expectedError, err.Error())
	}
}
