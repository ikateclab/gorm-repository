# Documentação: Correção de JSONB Aninhado

## 📚 Índice de Documentos

### 🎯 Comece Aqui
- **[JSONB_NESTED_DIFF_SUMMARY.md](JSONB_NESTED_DIFF_SUMMARY.md)** - Resumo executivo e roadmap

### 📖 Para Entender o Problema
- **[JSONB_NESTED_DIFF_PROBLEM.md](JSONB_NESTED_DIFF_PROBLEM.md)** - Demonstração detalhada do problema e solução

### 🔧 Para Implementar no gorm-tracked-updates
- **[JSONB_NESTED_DIFF_IMPLEMENTATION_GUIDE.md](JSONB_NESTED_DIFF_IMPLEMENTATION_GUIDE.md)** - Guia completo de implementação
- **[JSONB_NESTED_DIFF_TEST_CASES.md](JSONB_NESTED_DIFF_TEST_CASES.md)** - Casos de teste para validação

### 🛠️ Para Implementar no gorm-repository
- **[JSONB_NESTED_GORM_REPOSITORY_CHANGES.md](JSONB_NESTED_GORM_REPOSITORY_CHANGES.md)** - Mudanças necessárias neste repo

---

## 🚀 Guia Rápido

### 1. Entenda o Problema (5 min)
Leia: `JSONB_NESTED_DIFF_SUMMARY.md` → Seção "Problema"

### 2. Implemente no gorm-tracked-updates (4-6h)
1. Leia: `JSONB_NESTED_DIFF_PROBLEM.md`
2. Leia: `JSONB_NESTED_DIFF_IMPLEMENTATION_GUIDE.md`
3. Implemente a correção
4. Valide com: `JSONB_NESTED_DIFF_TEST_CASES.md`

### 3. Implemente no gorm-repository (3-4h)
1. Leia: `JSONB_NESTED_GORM_REPOSITORY_CHANGES.md`
2. Implemente as funções
3. Adicione testes de integração

### 4. Valide End-to-End (1-2h)
1. Regenere código com gerador corrigido
2. Execute todos os testes
3. Teste com caso real

---

## 📋 Ordem de Leitura Recomendada

```
1. JSONB_NESTED_DIFF_SUMMARY.md (visão geral)
   ↓
2. JSONB_NESTED_DIFF_PROBLEM.md (entender o problema)
   ↓
3. JSONB_NESTED_DIFF_IMPLEMENTATION_GUIDE.md (implementar gerador)
   ↓
4. JSONB_NESTED_DIFF_TEST_CASES.md (validar implementação)
   ↓
5. JSONB_NESTED_GORM_REPOSITORY_CHANGES.md (implementar repository)
```

---

## 🎯 Contexto para IA

Se você está usando esses documentos como contexto para uma IA (como Augment, Cursor, etc), forneça na seguinte ordem:

### Para corrigir gorm-tracked-updates:
```
1. JSONB_NESTED_DIFF_PROBLEM.md
2. JSONB_NESTED_DIFF_IMPLEMENTATION_GUIDE.md
3. JSONB_NESTED_DIFF_TEST_CASES.md
```

### Para corrigir gorm-repository:
```
1. JSONB_NESTED_DIFF_PROBLEM.md
2. JSONB_NESTED_GORM_REPOSITORY_CHANGES.md
```

---

## 📊 Resumo do Problema

**Sintoma:** `UpdateByIdInPlace()` perde dados em campos JSONB aninhados

**Causa:** Diff aninhado + operador `||` do PostgreSQL = shallow merge

**Solução:** Diff achatado + `jsonb_set` = deep merge

---

## ✅ Checklist Rápido

### gorm-tracked-updates
- [ ] Detectar campos JSONB aninhados
- [ ] Gerar diff achatado (dot notation)
- [ ] Testes passando

### gorm-repository
- [ ] Processar paths achatados
- [ ] Gerar `jsonb_set` aninhado
- [ ] Testes de integração passando

---

## 🔗 Links Úteis

- PostgreSQL `jsonb_set`: https://www.postgresql.org/docs/current/functions-json.html
- GORM Docs: https://gorm.io/docs/
- Sonic JSON: https://github.com/bytedance/sonic

---

Criado em: 2025-10-29
Última atualização: 2025-10-29

