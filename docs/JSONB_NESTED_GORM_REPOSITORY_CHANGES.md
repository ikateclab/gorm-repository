# Required Changes in gorm-repository

## Objective

After fixing the `gorm-tracked-updates` generator to produce flattened diffs, the `gorm-repository` needs to be modified to process these diffs and convert them into PostgreSQL `jsonb_set` queries.

---

## Summary of Changes

1. Add `processJSONBDiff()` function - detects flattened paths (dot notation) and groups them by root field
2. Add `buildJSONBSetExpression()` function - builds nested `jsonb_set` expression for PostgreSQL
3. Add `getJSONColumnType()` function - detects if column is `json` or `jsonb` type
4. Modify `UpdateByIdInPlace()` to use JSONB processing
5. Modify `UpdateById()` to use JSONB processing
6. Modify `UpdateInPlace()` to use JSONB processing
7. Add integration test `TestGormRepository_UpdateByIdInPlace_NestedJSONB`

---

## Implementation

### 1. Function: Process JSONB Diff

**File:** `gorm_repository.go`

**Purpose:** Detect flattened paths (keys with ".") in the diff map, group them by root field, and convert to `jsonb_set` expressions.

```go
// processJSONBDiff processes a diff map and converts flattened JSONB paths (dot notation)
// into jsonb_set expressions for PostgreSQL
func processJSONBDiff(db *gorm.DB, model interface{}, diff map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	grouped := make(map[string]map[string]interface{})

	// Get schema from the model
	stmt := &gorm.Statement{DB: db}
	stmt.Parse(model)

	// Group flattened paths by their root field name
	for key, value := range diff {
		if strings.Contains(key, ".") {
			// This is a flattened JSONB path like "status.mode" or "status.state.code"
			parts := strings.SplitN(key, ".", 2)
			fieldName := parts[0] // e.g., "status"
			subPath := parts[1]   // e.g., "mode" or "state.code"

			if grouped[fieldName] == nil {
				grouped[fieldName] = make(map[string]interface{})
			}
			grouped[fieldName][subPath] = value
		} else {
			// Regular field (not a flattened JSONB path)
			result[key] = value
		}
	}

	// Convert grouped paths into jsonb_set expressions
	for fieldName, paths := range grouped {
		// Get the field to find the actual struct field name (PascalCase)
		field := stmt.Schema.LookUpField(fieldName)
		if field == nil && len(fieldName) > 0 {
			// Try capitalizing the first letter (camelCase -> PascalCase)
			pascalCase := strings.ToUpper(fieldName[:1]) + fieldName[1:]
			field = stmt.Schema.LookUpField(pascalCase)
		}

		// Use the struct field name (PascalCase) as the key in the result map
		// GORM will handle the conversion to column name
		var resultKey string
		if field != nil {
			resultKey = field.Name // Use the struct field name (e.g., "WhatsAppData")
		} else {
			resultKey = fieldName // Fallback to the original field name
		}

		result[resultKey] = buildJSONBSetExpression(db, stmt.Schema, fieldName, paths)
	}

	return result
}
```

**Key points:**
- Accepts `model` parameter to get GORM schema
- Detects flattened paths by checking if key contains "."
- Groups paths by root field name (e.g., "status.mode" and "status.state" → grouped under "status")
- Uses GORM schema to convert field names (camelCase → PascalCase → column name)

---

### 2. Function: Build jsonb_set Expression

**File:** `gorm_repository.go`

**Purpose:** Build nested `jsonb_set` expressions to update multiple paths within a JSONB column.

```go
// buildJSONBSetExpression constructs a nested jsonb_set expression for PostgreSQL
// to update multiple paths within a JSONB column
func buildJSONBSetExpression(db *gorm.DB, schema *schema.Schema, fieldName string, paths map[string]interface{}) clause.Expr {
	// Get the field from the schema to find the actual column name
	// Try both camelCase and PascalCase versions
	field := schema.LookUpField(fieldName)
	if field == nil && len(fieldName) > 0 {
		// Try capitalizing the first letter (camelCase -> PascalCase)
		pascalCase := strings.ToUpper(fieldName[:1]) + fieldName[1:]
		field = schema.LookUpField(pascalCase)
	}

	var columnName string
	if field != nil {
		columnName = field.DBName
	} else {
		// Fallback: use the field name as-is
		columnName = fieldName
	}
	columnType := getJSONColumnType(db, schema.Table, columnName)

	// Start with the original column value (or empty object if NULL)
	expr := fmt.Sprintf("COALESCE(?::%s, '{}'::jsonb)", columnType)
	args := []interface{}{clause.Column{Name: columnName}}

	// Sort paths for consistent ordering
	sortedPaths := make([]string, 0, len(paths))
	for path := range paths {
		sortedPaths = append(sortedPaths, path)
	}
	sort.Strings(sortedPaths)

	// Build nested jsonb_set calls for each path
	for _, path := range sortedPaths {
		value := paths[path]

		// Convert "mode" or "state.code" to PostgreSQL array format
		// "mode" -> {mode}
		// "state.code" -> {state,code}
		pathParts := strings.Split(path, ".")
		pathArray := "{" + strings.Join(pathParts, ",") + "}"

		// Serialize value to JSON
		valueJSON, err := json.Marshal(value)
		if err != nil {
			// Skip this path if we can't marshal the value
			continue
		}

		// Nest another jsonb_set call
		expr = fmt.Sprintf("jsonb_set(%s, '%s', ?::jsonb)", expr, pathArray)
		args = append(args, string(valueJSON))
	}

	return gorm.Expr(expr, args...)
}
```

**Key points:**
- Accepts GORM `schema` instead of table name string
- Uses schema to lookup field and get database column name
- Handles both camelCase and PascalCase field names
- Detects column type (json vs jsonb) using `getJSONColumnType()`
- Builds nested `jsonb_set()` calls for each path

---

### 3. Function: Get JSON Column Type

**File:** `gorm_repository.go`

**Purpose:** Query PostgreSQL information_schema to determine if a column is `json` or `jsonb` type.

```go
// getJSONColumnType queries the database to determine if a column is json or jsonb
func getJSONColumnType(db *gorm.DB, tableName, columnName string) string {
	var dataType string
	query := `
		SELECT data_type
		FROM information_schema.columns
		WHERE table_name = ? AND column_name = ?
	`
	db.Raw(query, tableName, columnName).Scan(&dataType)

	if dataType == "json" {
		return "json"
	}
	return "jsonb" // Default to jsonb
}
```

**Key points:**
- Queries PostgreSQL `information_schema.columns` table
- Returns "json" or "jsonb" based on actual column type
- Defaults to "jsonb" if not found

### 4. Modify UpdateByIdInPlace

**File:** `gorm_repository.go`

**Change:** Add call to `processJSONBDiff()` with entity model parameter.

```go
func (r *GormRepository[T]) UpdateByIdInPlace(ctx context.Context, id uuid.UUID, entity *T, updateFunc func(), options ...Option) error {
    db := applyOptions(r.DB, options).WithContext(ctx)

    diffable, isDiffable := any(entity).(Diffable[T])
    if !isDiffable {
        return fmt.Errorf("entity does not support diffing - entity must implement Diffable[T] interface")
    }

    // Clone the original entity to use for diff generation
    originalClone := diffable.Clone()

    // Apply the update function to modify the entity in place
    updateFunc()

    diff := diffable.Diff(originalClone)

    if len(diff) == 0 {
        // No changes, nothing to update
        return nil
    }

    // NEW: Process diff to convert flattened paths to jsonb_set
    processedDiff := processJSONBDiff(db, entity, diff)

    // Perform the update using the processed diff
    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Where("id = ?", id).Updates(processedDiff).Error
}
```

---

### 5. Modify UpdateById

**File:** `gorm_repository.go`

**Change:** Add call to `processJSONBDiff()` with entity model parameter.

```go
func (r *GormRepository[T]) UpdateById(ctx context.Context, id uuid.UUID, entity *T, options ...Option) error {
    db := applyOptions(r.DB, options).WithContext(ctx)

    // Generate diff
    diffable, ok := any(entity).(Diffable[T])
    if !ok {
        return fmt.Errorf("entity must implement Diffable[T] interface")
    }

    clone := getCloneForDiff(db, entity)

    diff := diffable.Diff(clone)
    if len(diff) == 0 {
        return nil // No changes
    }

    // NEW: Process diff to convert flattened paths to jsonb_set
    processedDiff := processJSONBDiff(db, entity, diff)

    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Where("id = ?", id).Updates(processedDiff).Error
}
```

---

### 6. Modify UpdateInPlace

**File:** `gorm_repository.go`

**Change:** Add call to `processJSONBDiff()` with entity model parameter.

```go
func (r *GormRepository[T]) UpdateInPlace(ctx context.Context, entity *T, updateFunc func(), options ...Option) error {
    db := applyOptions(r.DB, options).WithContext(ctx)

    diffable, isDiffable := any(entity).(Diffable[T])
    if !isDiffable {
        return fmt.Errorf("entity does not support diffing - entity must implement Diffable[T] interface")
    }

    // Clone the original entity to use for diff generation
    originalClone := diffable.Clone()

    // Apply the update function to modify the entity in place
    updateFunc()

    diff := diffable.Diff(originalClone)

    if len(diff) == 0 {
        // No changes, nothing to update
        return nil
    }

    // NEW: Process diff to convert flattened paths to jsonb_set
    processedDiff := processJSONBDiff(db, entity, diff)

    // Perform the update using the diff - GORM will extract the primary key from the entity
    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Updates(processedDiff).Error
}
```

---

## Integration Tests

### Test: UpdateByIdInPlace with Nested JSONB

**File:** `gorm_repository_test.go`

This test validates that updating a single nested JSONB field preserves all other nested fields.

```go
func TestGormRepository_UpdateByIdInPlace_NestedJSONB(t *testing.T) {
    db := setupTestDB(t)
    repo := &GormRepository[tests.TestUser]{DB: db}
    ctx := context.Background()

    user := createTestUser()
    user.WhatsAppData = &tests.WhatsAppData{
        Error: "",
        Status: &tests.WhatsAppStatus{
            Mode:            "QR",
            State:           "NORMAL",
            IsStarted:       true,
            WaVersion:       "2.3000.1029001831",
            IsOnQrPage:      true,
            IsWebConnected:  true,
            QrCodeExpiresAt: "2025-10-29T19:13:46.878Z",
            QrCodeUrl:       "https://example.com/qr-code.png",
        },
        DriverId: "a20b69a8-ba35-4d84-83be-933a5544935d",
    }

    err := repo.Create(ctx, user)
    require.NoError(t, err, "Failed to create test user")

    // Update ONLY the Mode field in the nested Status
    err = repo.UpdateByIdInPlace(ctx, user.Id, user, func() {
        user.WhatsAppData.Status.Mode = "CONNECTED"
    })
    require.NoError(t, err, "UpdateByIdInPlace should not fail")

    // Verify all other fields are preserved
    updated, err := repo.FindById(ctx, user.Id)
    require.NoError(t, err)
    assert.Equal(t, "CONNECTED", updated.WhatsAppData.Status.Mode)
    assert.Equal(t, "NORMAL", updated.WhatsAppData.Status.State)
    assert.True(t, updated.WhatsAppData.Status.IsStarted)
    assert.Equal(t, "2.3000.1029001831", updated.WhatsAppData.Status.WaVersion)
    assert.True(t, updated.WhatsAppData.Status.IsOnQrPage)
    assert.True(t, updated.WhatsAppData.Status.IsWebConnected)
    assert.Equal(t, "2025-10-29T19:13:46.878Z", updated.WhatsAppData.Status.QrCodeExpiresAt)
    assert.Equal(t, "https://example.com/qr-code.png", updated.WhatsAppData.Status.QrCodeUrl)
    assert.Equal(t, "", updated.WhatsAppData.Error)
    assert.Equal(t, "a20b69a8-ba35-4d84-83be-933a5544935d", updated.WhatsAppData.DriverId)
}
```

---

## Generated SQL Examples

### Before Fix (WRONG - Loses Data)

When updating `user.WhatsAppData.Status.Mode = "CONNECTED"`:

```sql
UPDATE "test_users"
SET "whats_app_data" = "whats_app_data" || '{"status": {"mode": "CONNECTED"}}'::jsonb
WHERE id = ?
```

**Problem:** PostgreSQL's `||` operator does a **shallow merge**, replacing the entire `status` object with `{"mode": "CONNECTED"}`, losing all other fields like `state`, `isStarted`, `waVersion`, etc.

---

### After Fix (CORRECT - Preserves Data)

With the flattened diff approach:

```sql
UPDATE "test_users"
SET "whats_app_data" = jsonb_set(
    COALESCE("whats_app_data"::jsonb, '{}'::jsonb),
    '{status,mode}',
    '"CONNECTED"'::jsonb
)
WHERE id = ?
```

**Solution:** PostgreSQL's `jsonb_set()` function does a **deep merge**, updating only the specific path `{status,mode}` while preserving all other fields in the `status` object.

---

## How It Works

### 1. Diff Generation (gorm-tracked-updates)

The `gorm-tracked-updates` generator creates flattened diffs:

```go
// Before: nested diff (WRONG)
diff := map[string]interface{}{
    "whatsAppData": map[string]interface{}{
        "status": map[string]interface{}{
            "mode": "CONNECTED",
        },
    },
}

// After: flattened diff (CORRECT)
diff := map[string]interface{}{
    "whatsAppData.status.mode": "CONNECTED",
}
```

### 2. Diff Processing (gorm-repository)

The `processJSONBDiff()` function:

1. Detects flattened paths (keys containing ".")
2. Groups them by root field: `"whatsAppData.status.mode"` → root: `"whatsAppData"`, path: `"status.mode"`
3. Converts to `jsonb_set` expression

### 3. SQL Generation

The `buildJSONBSetExpression()` function:

1. Converts dot notation to PostgreSQL array: `"status.mode"` → `{status,mode}`
2. Builds nested `jsonb_set()` calls
3. Uses GORM schema to get correct column names

---

## Implementation Checklist

- [x] Add `processJSONBDiff()` function
- [x] Add `buildJSONBSetExpression()` function
- [x] Add `getJSONColumnType()` function
- [x] Modify `UpdateByIdInPlace()` to call `processJSONBDiff()`
- [x] Modify `UpdateById()` to call `processJSONBDiff()`
- [x] Modify `UpdateInPlace()` to call `processJSONBDiff()`
- [x] Add integration test `TestGormRepository_UpdateByIdInPlace_NestedJSONB`
- [x] Validate generated SQL
- [x] Update `gorm-tracked-updates` dependency
- [x] Update documentation

