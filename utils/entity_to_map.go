package utils

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Enhanced field info with index for faster access
type fieldInfo struct {
	ColumnName string
	Index      int
	IsPtr      bool
}

// Enhanced cache structures
var (
	typeCacheMutex sync.RWMutex
	fieldInfoCache = make(map[reflect.Type]map[string]fieldInfo)
	jsonNameCache  sync.Map
)

// Small field count optimization
const smallFieldCount = 4

// EntityToMap converts an entity into a map based on specified fields.
func EntityToMap(fields map[string]interface{}, entity interface{}) (map[string]interface{}, error) {
	entityValue := reflect.Indirect(reflect.ValueOf(entity))
	entityType := entityValue.Type()

	// Get or create the field info mapping
	fieldInfoMap := getFieldInfoMap(entityType)

	// Create the update map with exact capacity
	updateMap := make(map[string]interface{}, len(fields))

	// Fast path for small field counts (common case)
	if len(fields) <= smallFieldCount {
		// Process each field
		for key, value := range fields {
			info, found := fieldInfoMap[key]
			if !found {
				return nil, errors.New("field not found in entity: " + key)
			}

			// Get field by index instead of by name (much faster)
			fieldValue := entityValue.Field(info.Index)

			// Handle pointer types
			if info.IsPtr {
				if fieldValue.IsNil() {
					return nil, errors.New("nil pointer encountered for field: " + key)
				}
				fieldValue = fieldValue.Elem()
			}

			// Handle nested fields
			if subMap, ok := value.(map[string]interface{}); ok {
				subUpdateMap, err := handleNestedFields(fieldValue, subMap)
				if err != nil {
					return nil, err
				}

				jsonValue, err := json.Marshal(subUpdateMap)
				if err != nil {
					return nil, err
				}

				updateMap[info.ColumnName] = gorm.Expr("? || ?", clause.Column{Name: info.ColumnName}, string(jsonValue))
			} else {
				updateMap[info.ColumnName] = fieldValue.Interface()
			}
		}
		return updateMap, nil
	}

	// Regular path for larger field counts
	for key, value := range fields {
		info, found := fieldInfoMap[key]
		if !found {
			return nil, errors.New("field not found in entity: " + key)
		}

		// Get field by index instead of by name
		fieldValue := entityValue.Field(info.Index)

		if info.IsPtr {
			if fieldValue.IsNil() {
				return nil, errors.New("nil pointer encountered for field: " + key)
			}
			fieldValue = fieldValue.Elem()
		}

		if subMap, ok := value.(map[string]interface{}); ok {
			subUpdateMap, err := handleNestedFields(fieldValue, subMap)
			if err != nil {
				return nil, err
			}

			jsonValue, err := json.Marshal(subUpdateMap)
			if err != nil {
				return nil, err
			}

			updateMap[info.ColumnName] = gorm.Expr("? || ?", clause.Column{Name: info.ColumnName}, string(jsonValue))
		} else {
			updateMap[info.ColumnName] = fieldValue.Interface()
		}
	}

	return updateMap, nil
}

// Cache for column names
var columnNameCache sync.Map

// getFieldInfoMap retrieves or creates detailed field info mapping for a type
func getFieldInfoMap(entityType reflect.Type) map[string]fieldInfo {
	// Check cache first using read lock (faster)
	typeCacheMutex.RLock()
	fieldMap, found := fieldInfoCache[entityType]
	typeCacheMutex.RUnlock()

	if found {
		return fieldMap
	}

	// Not in cache, create mapping
	fieldMap = make(map[string]fieldInfo, entityType.NumField())
	namingStrategy := CamelCaseNamingStrategy{}

	// Add each field to the mapping
	for i := 0; i < entityType.NumField(); i++ {
		field := entityType.Field(i)

		// Skip unexported fields
		if field.PkgPath != "" {
			continue
		}

		// Calculate column name directly
		columnName := namingStrategy.ColumnName("", field.Name)

		fieldMap[field.Name] = fieldInfo{
			ColumnName: columnName,
			Index:      i,
			IsPtr:      field.Type.Kind() == reflect.Ptr,
		}
	}

	// Store in cache with write lock
	typeCacheMutex.Lock()
	// Double check if another goroutine already created the mapping
	if existing, found := fieldInfoCache[entityType]; found {
		typeCacheMutex.Unlock()
		return existing
	}
	fieldInfoCache[entityType] = fieldMap
	typeCacheMutex.Unlock()

	return fieldMap
}

// Enhanced field info cache for nested fields
type nestedFieldInfo struct {
	Index    int
	JSONName string
	IsPtr    bool
}

// Cache for nested field access
var nestedFieldCache sync.Map // map[reflect.Type]map[string]nestedFieldInfo

// handleNestedFields processes nested field structures
func handleNestedFields(fieldValue reflect.Value, subMap map[string]interface{}) (map[string]interface{}, error) {
	subUpdateMap := make(map[string]interface{}, len(subMap))

	switch fieldValue.Kind() {
	case reflect.Struct:
		fieldType := fieldValue.Type()

		// Get or create nested field info
		var nestedFields map[string]nestedFieldInfo

		// Check cache first
		if cached, found := nestedFieldCache.Load(fieldType); found {
			nestedFields = cached.(map[string]nestedFieldInfo)
		} else {
			// Create new mapping
			nestedFields = make(map[string]nestedFieldInfo, fieldType.NumField())

			for i := 0; i < fieldType.NumField(); i++ {
				field := fieldType.Field(i)

				// Skip unexported
				if field.PkgPath != "" {
					continue
				}

				jsonName := getJSONName(field)
				nestedFields[field.Name] = nestedFieldInfo{
					Index:    i,
					JSONName: jsonName,
					IsPtr:    field.Type.Kind() == reflect.Ptr,
				}
			}

			// Store in cache
			nestedFieldCache.Store(fieldType, nestedFields)
		}

		for subKey, subValue := range subMap {
			info, found := nestedFields[subKey]
			if !found {
				return nil, errors.New("field not found: " + subKey)
			}

			// Access field by index (faster than FieldByName)
			subFieldValue := fieldValue.Field(info.Index)

			// Handle pointer types
			if info.IsPtr {
				if subFieldValue.IsNil() {
					return nil, errors.New("nil pointer for field: " + subKey)
				}
				subFieldValue = subFieldValue.Elem()
			}

			// Handle nested maps recursively
			if nestedMap, ok := subValue.(map[string]interface{}); ok {
				nestedResult, err := handleNestedFields(subFieldValue, nestedMap)
				if err != nil {
					return nil, err
				}
				subUpdateMap[info.JSONName] = nestedResult
			} else {
				subUpdateMap[info.JSONName] = subFieldValue.Interface()
			}
		}

	case reflect.Map:
		for subKey, subValue := range subMap {
			keyValue := reflect.ValueOf(subKey)
			mapValue := fieldValue.MapIndex(keyValue)

			if mapValue.IsValid() {
				if nestedMap, ok := subValue.(map[string]interface{}); ok && mapValue.IsValid() {
					nestedResult, err := handleNestedFields(mapValue, nestedMap)
					if err != nil {
						return nil, err
					}
					subUpdateMap[subKey] = nestedResult
				} else {
					subUpdateMap[subKey] = mapValue.Interface()
				}
			} else {
				subUpdateMap[subKey] = nil
			}
		}

	default:
		return nil, errors.New("unsupported type for nested fields")
	}

	return subUpdateMap, nil
}

// getJSONName extracts the JSON field name from struct tags with caching
func getJSONName(field reflect.StructField) string {
	// Use unique key based on package path, struct and field name
	cacheKey := field.PkgPath + "." + field.Name

	// Check cache first
	if cachedName, ok := jsonNameCache.Load(cacheKey); ok {
		return cachedName.(string)
	}

	// Calculate JSON name
	tag := field.Tag.Get("json")
	var result string

	if tag == "" || tag == "-" {
		result = field.Name
	} else {
		// Get the part before the first comma
		if idx := strings.IndexByte(tag, ','); idx != -1 {
			tag = tag[:idx]
		}

		if tag == "" {
			result = field.Name
		} else {
			result = tag
		}
	}

	// Store in cache
	jsonNameCache.Store(cacheKey, result)
	return result
}
