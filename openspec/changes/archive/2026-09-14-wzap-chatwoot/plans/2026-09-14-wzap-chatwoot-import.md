# Wzap Chatwoot Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import histórico WhatsApp→Chatwoot via SQL direto no Postgres do Chatwoot, com dedup, janela e 3 gatilhos.

**Architecture:** Módulo isolado `internal/chatwoot/import` com pool `pgx` próprio, guard por URI, alimentado pelos acumuladores do feed (Plano 1, Task 4); cron de 30min no worker.

**Tech Stack:** Go 1.26, `pgx/v5` (já direta), resto do Plano 1.

**Spec:** `openspec/changes/wzap-chatwoot/specs/wzap-chatwoot-import/spec.md` (+ design.md, seção import).

## Global Constraints

- Pré-requisito: Plano 1 verde (feed, acumuladores, conversa operacional, `POST .../chatwoot/import` pode nascer aqui se preferir — este plano o cria).
- Sem URI válida: módulo inerte, zero SQL tentado, resto intacto.
- TDD, commit por task; `gofmt -l .` vazio, `go vet ./...`, `golangci-lint run`, `go test ./... -count=1`, `go build ./...`.
- Recriar do Evolution, nunca copiar (atenção aos bugs conhecidos: insert de tags 2×, `try connected` no-op).
- pt-BR na prosa, inglês no código.

---

## File Structure

**Criar:**
- `internal/chatwoot/import/pg.go` (+ test) — pool único por processo, guard por URI.
- `internal/chatwoot/import/import.go` (+ test) — contatos, mensagens, dedup, janela, placeholders.
- `internal/chatwoot/import/scheduler.go` (+ test) — cron 30min `syncLostMessages` (janela 6h).
- `internal/httpapi/chatwoot.go` (+ test, modify) — `POST /instances/{id}/chatwoot/import`.

**Modificar:** `cmd/wzap/main.go` (fia pool + cron + rota), `internal/chatwoot/mirror/worker.go` (dispara auto pós-pareamento).

---

### Task 1: Pool com guard + import de contatos

**Files:**
- Create: `internal/chatwoot/import/pg.go`, `pg_test.go`, `import.go`, `import_test.go`

**Interfaces:**
- Consumes: acumuladores do feed (`[]Contact`, `[]HistoryMessage` por instância), `WZAP_CHATWOOT_IMPORT_DB_URL`
- Produces: `chatimport.Pool(ctx) (*pgxpool.Pool, bool)` (false = inerte), `chatimport.ImportContacts(ctx, pool, accountID, inboxName string, contacts []Contact) (int, error)`

- [ ] **Step 1: teste de guard falhando.** Sem URI → `Pool` retorna `ok=false` e `ImportContacts` retorna `0, nil` sem tocar rede. Com URI inválida → erro nomeando a operação. Run: `go test ./internal/chatwoot/import/ -count=1 -v`. Expected: FAIL.
- [ ] **Step 2: implementar `pg.go`.** `pgxpool.New` com `sslmode` da URI; pool singleton com `sync.Once` + `Close` no shutdown; SSL sem verificação só se a URI pedir (documentar). Run. Expected: PASS (guard parte).
- [ ] **Step 3: teste de contatos falhando.** Contra Postgres real de teste (`_test`) com schema mínimo Chatwoot (`labels, contacts, tags, taggings` criados no teste): 2 contatos + 1 grupo → upsert por `(identifier, account_id)`, grupo com `phone NULL` e nome `+ " (GROUP)"`, taggings criadas uma vez (reexecução não duplica). Run com `WZAP_TEST_DATABASE_URL`. Expected: FAIL.
- [ ] **Step 4: implementar `ImportContacts`** (chunks 3000, binds `$n`, `ON CONFLICT(identifier, account_id) DO UPDATE`, single insert de tags — sem o duplo do original). Run. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(chatwoot): import de contatos com guard"`.

### Task 2: Import de mensagens + gatilhos

**Files:**
- Create/Modify: `internal/chatwoot/import/import.go`, `scheduler.go`, tests; Modify `mirror/worker.go`, `httpapi/chatwoot.go`, `cmd/wzap/main.go`

**Interfaces:**
- Consumes: `client.Client` (inbox), `chatimport` Task 1, feed
- Produces: `chatimport.ImportMessages(ctx, deps, inboxID int64, msgs []HistoryMessage) (int, error)`, `scheduler.Start(ctx)` (cron 30min), `POST /instances/{id}/chatwoot/import` → `202 {imported}`

- [ ] **Step 1: teste de mensagens falhando.** Ordem por telefone+tempo, dedup por `source_id` pré-existente (`WAID:` normalizado), `fromMe`→`message_type 1` + sender do `access_tokens`, lotes, `days_limit` corta, sem conteúdo pula, placeholder quando flag. Run. Expected: FAIL.
- [ ] **Step 2: implementar `ImportMessages`** (CTE de FKs adaptada, `to_timestamp`, propaga `source_id` dos envios do espelho). Run. Expected: PASS.
- [ ] **Step 3: teste de gatilhos falhando.** Auto pós-pareamento dispara uma vez; `POST .../import` retorna contagem; cron 30min chama `syncLostMessages` (janela 6h) e limpa acumuladores; avisos na conversa operacional. Run. Expected: FAIL.
- [ ] **Step 4: implementar gatilhos + rota + fiação** (cron para antes do relay no shutdown). Run. Expected: PASS.
- [ ] **Step 5: gates + docs.** `gofmt -l .`, `go vet ./...`, `golangci-lint run`, suite completa com Postgres, `go build ./...`; atualizar README/AGENTS.md (seção Chatwoot: envs, rotas, riscos token claro/webhook aberto/SQL direto). Run tudo. Expected: verde.
- [ ] **Step 6: Commit.** `git commit -m "feat(chatwoot): import de mensagens e gatilhos"`.

## Self-Review

- Spec coberta: guard, acumuladores+dedup, gatilhos, janela/placeholders — cada uma com task.
- Sem placeholders: SQL/assinaturas/comandos exatos acima.
- Consistência: `chatimport` + nomes definidos na Task 1 e reusados na 2.
