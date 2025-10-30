# Mudanças Necessárias no gorm-repository

## 🎯 Objetivo

Após corrigir o gerador `gorm-tracked-updates` para produzir diffs achatados, o `gorm-repository` precisa ser modificado para processar esses diffs e convertê-los em queries `jsonb_set` do PostgreSQL.

---

## 📋 Resumo das Mudanças

1. ✅ Adicionar função `processJSONBPaths()` - detecta e processa paths achatados
2. ✅ Adicionar função `buildJSONBSetExpression()` - constrói expressão `jsonb_set` aninhada
3. ✅ Modificar `UpdateByIdInPlace()` para usar o processamento
4. ✅ Modificar `UpdateById()` para usar o processamento
5. ✅ Modificar `UpdateInPlace()` para usar o processamento
6. ✅ Adicionar testes de integração

---

## 🔧 Implementação

### 1. Função: Detectar Paths Achatados

**Arquivo:** `gorm_repository.go`

```go
// hasJSONBPaths verifica se um map contém paths achatados (dot notation)
func hasJSONBPaths(m map[string]interface{}) bool {
    for key := range m {
        if strings.Contains(key, ".") {
            return true
        }
    }
    return false
}
```

### 2. Função: Processar Diff com Paths JSONB

**Arquivo:** `gorm_repository.go`

```go
// processJSONBDiff processa um diff que pode conter paths achatados
// e converte para expressões jsonb_set do PostgreSQL
func processJSONBDiff(db *gorm.DB, diff map[string]interface{}) map[string]interface{} {
    result := make(map[string]interface{})
    
    for key, value := range diff {
        // Verificar se o valor é um map (possível JSONB)
        if mapValue, ok := value.(map[string]interface{}); ok {
            // Verificar se tem paths achatados
            if hasJSONBPaths(mapValue) {
                // Converter para jsonb_set aninhado
                tableName := getTableName(db)
                result[key] = buildJSONBSetExpression(db, tableName, key, mapValue)
            } else {
                // JSONB normal - usar merge com ||
                jsonValue, err := sonic.Marshal(mapValue)
                if err == nil && !isEmptyJSON(string(jsonValue)) {
                    tableName := getTableName(db)
                    result[key] = BuildJSONMergeExpr(db, tableName, key, string(jsonValue))
                } else {
                    result[key] = value
                }
            }
        } else {
            // Campo normal (não JSONB)
            result[key] = value
        }
    }
    
    return result
}
```

### 3. Função: Construir Expressão jsonb_set

**Arquivo:** `gorm_repository.go`

```go
// buildJSONBSetExpression constrói uma expressão jsonb_set aninhada
// para atualizar múltiplos paths em um campo JSONB
func buildJSONBSetExpression(db *gorm.DB, tableName, columnName string, paths map[string]interface{}) clause.Expr {
    columnType := getJSONColumnType(db, tableName, columnName)
    
    // Começar com a coluna original
    expr := fmt.Sprintf("COALESCE(?::%s, '{}'::jsonb)", columnType)
    args := []interface{}{clause.Column{Name: columnName}}
    
    // Ordenar paths para garantir consistência
    sortedPaths := make([]string, 0, len(paths))
    for path := range paths {
        sortedPaths = append(sortedPaths, path)
    }
    sort.Strings(sortedPaths)
    
    // Para cada path, aninhar jsonb_set
    for _, path := range sortedPaths {
        value := paths[path]
        
        // Converter "status.mode" -> {status,mode}
        pathParts := strings.Split(path, ".")
        pathArray := "{" + strings.Join(pathParts, ",") + "}"
        
        // Serializar valor para JSON
        valueJSON, err := sonic.Marshal(value)
        if err != nil {
            // Fallback: usar valor direto
            continue
        }
        
        // Aninhar jsonb_set
        expr = fmt.Sprintf("jsonb_set(%s, '%s', ?::jsonb)", expr, pathArray)
        args = append(args, string(valueJSON))
    }
    
    return gorm.Expr(expr, args...)
}
```

### 4. Função Helper: Obter Nome da Tabela

**Arquivo:** `gorm_repository.go`

```go
// getTableName obtém o nome da tabela a partir do DB statement
func getTableName(db *gorm.DB) string {
    stmt := &gorm.Statement{DB: db}
    
    // Tentar obter do modelo
    if db.Statement != nil && db.Statement.Model != nil {
        stmt.Parse(db.Statement.Model)
        return stmt.Table
    }
    
    // Fallback: retornar vazio (será tratado depois)
    return ""
}
```

### 5. Função Helper: Verificar JSON Vazio

**Arquivo:** `gorm_repository.go`

```go
// isEmptyJSON verifica se uma string JSON representa um objeto/array vazio
func isEmptyJSON(jsonStr string) bool {
    trimmed := strings.TrimSpace(jsonStr)
    return trimmed == "{}" || trimmed == "[]" || trimmed == "null" || trimmed == ""
}
```

### 6. Modificar UpdateByIdInPlace

**Arquivo:** `gorm_repository.go`

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

    // ✅ NOVO: Processar diff para converter paths achatados em jsonb_set
    processedDiff := processJSONBDiff(db, diff)

    // Perform the update using the processed diff
    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Where("id = ?", id).Updates(processedDiff).Error
}
```

### 7. Modificar UpdateById

**Arquivo:** `gorm_repository.go`

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

    // ✅ NOVO: Processar diff para converter paths achatados em jsonb_set
    processedDiff := processJSONBDiff(db, diff)

    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Where("id = ?", id).Updates(processedDiff).Error
}
```

### 8. Modificar UpdateInPlace

**Arquivo:** `gorm_repository.go`

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

    // ✅ NOVO: Processar diff para converter paths achatados em jsonb_set
    processedDiff := processJSONBDiff(db, diff)

    // Perform the update using the diff - GORM will extract the primary key from the entity
    return db.Model(entity).Omit(clause.Associations).Clauses(clause.Returning{}).Updates(processedDiff).Error
}
```

---

## 🧪 Testes de Integração

### Teste 1: UpdateByIdInPlace com JSONB Aninhado

**Arquivo:** `gorm_repository_test.go`

```go
func TestGormRepository_UpdateByIdInPlace_NestedJSONB(t *testing.T) {
    db := setupTestDB(t)
    repo := &GormRepository[tests.TestUser]{DB: db}
    ctx := context.Background()

    // Criar usuário com WhatsAppData
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

    // Atualizar APENAS o campo Mode
    err = repo.UpdateByIdInPlace(ctx, user.Id, user, func() {
        user.WhatsAppData.Status.Mode = "CONNECTED"
    })
    require.NoError(t, err, "UpdateByIdInPlace should not fail")

    // Verificar que TODOS os campos foram preservados
    updatedUser, err := repo.FindById(ctx, user.Id)
    require.NoError(t, err, "Failed to find updated user")

    require.Equal(t, "CONNECTED", updatedUser.WhatsAppData.Status.Mode, "Mode should be updated")
    require.Equal(t, "NORMAL", updatedUser.WhatsAppData.Status.State, "State should be preserved")
    require.Equal(t, true, updatedUser.WhatsAppData.Status.IsStarted, "IsStarted should be preserved")
    require.Equal(t, "2.3000.1029001831", updatedUser.WhatsAppData.Status.WaVersion, "WaVersion should be preserved")
    require.Equal(t, true, updatedUser.WhatsAppData.Status.IsOnQrPage, "IsOnQrPage should be preserved")
    require.Equal(t, true, updatedUser.WhatsAppData.Status.IsWebConnected, "IsWebConnected should be preserved")
    require.Equal(t, "2025-10-29T19:13:46.878Z", updatedUser.WhatsAppData.Status.QrCodeExpiresAt, "QrCodeExpiresAt should be preserved")
    require.Equal(t, "https://example.com/qr-code.png", updatedUser.WhatsAppData.Status.QrCodeUrl, "QrCodeUrl should be preserved")
    require.Equal(t, "", updatedUser.WhatsAppData.Error, "Error should be preserved")
    require.Equal(t, "a20b69a8-ba35-4d84-83be-933a5544935d", updatedUser.WhatsAppData.DriverId, "DriverId should be preserved")
}
```

### Teste 2: Múltiplos Campos Aninhados

```go
func TestGormRepository_UpdateByIdInPlace_NestedJSONB_MultipleFields(t *testing.T) {
    db := setupTestDB(t)
    repo := &GormRepository[tests.TestUser]{DB: db}
    ctx := context.Background()

    user := createTestUser()
    user.WhatsAppData = &tests.WhatsAppData{
        Status: &tests.WhatsAppStatus{
            Mode:  "QR",
            State: "NORMAL",
        },
    }
    
    err := repo.Create(ctx, user)
    require.NoError(t, err)

    // Atualizar múltiplos campos
    err = repo.UpdateByIdInPlace(ctx, user.Id, user, func() {
        user.WhatsAppData.Status.Mode = "CONNECTED"
        user.WhatsAppData.Status.State = "ACTIVE"
    })
    require.NoError(t, err)

    updatedUser, err := repo.FindById(ctx, user.Id)
    require.NoError(t, err)

    require.Equal(t, "CONNECTED", updatedUser.WhatsAppData.Status.Mode)
    require.Equal(t, "ACTIVE", updatedUser.WhatsAppData.Status.State)
}
```

---

## 📊 SQL Gerado (Exemplo)

### Antes da Correção (ERRADO)

```sql
UPDATE users 
SET whatsAppData = whatsAppData || '{"status": {"mode": "CONNECTED"}}'::jsonb
WHERE id = ?
-- Resultado: PERDE todos os outros campos do status
```

### Depois da Correção (CORRETO)

```sql
UPDATE users 
SET whatsAppData = jsonb_set(
    jsonb_set(
        jsonb_set(
            COALESCE(whatsAppData::jsonb, '{}'::jsonb),
            '{status,mode}', '"CONNECTED"'::jsonb
        ),
        '{status,state}', '"NORMAL"'::jsonb
    ),
    '{status,isStarted}', 'true'::jsonb
    -- ... todos os outros campos
)
WHERE id = ?
-- Resultado: PRESERVA todos os campos
```

---

## ✅ Checklist de Implementação

- [ ] Adicionar `hasJSONBPaths()`
- [ ] Adicionar `processJSONBDiff()`
- [ ] Adicionar `buildJSONBSetExpression()`
- [ ] Adicionar `getTableName()`
- [ ] Adicionar `isEmptyJSON()`
- [ ] Modificar `UpdateByIdInPlace()`
- [ ] Modificar `UpdateById()`
- [ ] Modificar `UpdateInPlace()`
- [ ] Adicionar teste de integração básico
- [ ] Adicionar teste com múltiplos campos
- [ ] Adicionar teste com múltiplos níveis
- [ ] Validar SQL gerado
- [ ] Atualizar documentação

---

## 🎯 Ordem de Implementação

1. **Primeiro:** Corrigir `gorm-tracked-updates` (gerar diffs achatados)
2. **Segundo:** Implementar funções helper aqui (`hasJSONBPaths`, `buildJSONBSetExpression`, etc)
3. **Terceiro:** Modificar métodos de update (`UpdateByIdInPlace`, etc)
4. **Quarto:** Adicionar testes de integração
5. **Quinto:** Validar com caso real (WhatsApp)

