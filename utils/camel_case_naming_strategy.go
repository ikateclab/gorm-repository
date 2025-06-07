package utils

import (
	"strings"
	"unicode"

	"gorm.io/gorm/schema"
)

// CamelCaseNamingStrategy implements the schema.Namer interface to use camelCase
type CamelCaseNamingStrategy struct{}

// TableName converts table names to camelCase
func (CamelCaseNamingStrategy) TableName(table string) string {
	return table
}

// SchemaName returns the schema name
func (CamelCaseNamingStrategy) SchemaName(schema string) string {
	return schema
}

// ColumnName converts column names to camelCase with the first character lower case
func (CamelCaseNamingStrategy) ColumnName(table, column string) string {
	if column == "" {
		return column
	}
	return toLowerCamelCase(column)
}

// JoinTableName returns the join table name
func (CamelCaseNamingStrategy) JoinTableName(joinTable string) string {
	return joinTable
}

// RelationshipFKName returns the foreign key name
func (CamelCaseNamingStrategy) RelationshipFKName(rel schema.Relationship) string {
	return strings.ToLower(rel.Name) + "_" + strings.ToLower(rel.Field.Name) + "_fkey"
}

// CheckerName returns the checker name
func (CamelCaseNamingStrategy) CheckerName(table, column string) string {
	return "chk_" + table + "_" + column
}

// IndexName returns the index name
func (CamelCaseNamingStrategy) IndexName(table, column string) string {
	return "idx_" + table + "_" + column
}

// UniqueName returns the unique constraint name
func (CamelCaseNamingStrategy) UniqueName(table, column string) string {
	return "uq_" + table + "_" + column
}

// toLowerCamelCase converts a string to camelCase with the first character in lower case
func toLowerCamelCase(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if i == 0 {
			runes[i] = unicode.ToLower(r)
		} else if unicode.IsUpper(r) {
			runes[i] = unicode.ToLower(r)
		} else {
			break
		}
	}
	return string(runes)
}
