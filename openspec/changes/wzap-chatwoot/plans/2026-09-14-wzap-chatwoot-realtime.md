# Wzap Chatwoot Realtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Conector Chatwoot em tempo real em `internal/chatwoot`: config por instância, espelho WA→Chatwoot via consumer durável, entrada Chatwoot→WA via webhook aberto, contatos/conversas, extensão de sessão, fiação e gates verdes.

**Architecture:** Worker `chatwoot` como consumer JetStream durável in-process (só leitura, sem segundo relay); webhook reusa `message.Service.Enqueue`; sessão/whatsmeow ganha edit/delete/mark-read/pairing-code/history-feed; cliente HTTP Chatwoot hand-rolled; locks por remetente em locker próprio.

**Tech Stack:** Go 1.26 (`go 1.26.0`), `pgx/v5`, NATS JetStream (`nats.go`), goose (migrações embutidas), `nyaruka/phonenumbers` + `golang.org/x/image` (novas, pinadas), `google/uuid`.

**Spec:** `openspec/changes/wzap-chatwoot/` — `proposal.md`, `design.md`, `specs/wzap-chatwoot-config/spec.md`, `specs/wzap-chatwoot-mirror/spec.md`, `specs/wzap-chatwoot-inbound/spec.md`, `specs/wzap-chatwoot-contacts/spec.md`, `specs/wzap-inbound-events/spec.md`. Import (`specs/wzap-chatwoot-import/spec.md`) fica para o Plano 2.

## Global Constraints

- Pré-requisito: `wzap-product` aplicado (rotas na raiz + auth `apikey:` dual; este plano consome `auth.Scope`, não Bearer).
- Comunicação pt-BR; código, identificadores e logs em inglês.
- TDD sempre: teste falhando → implementação mínima → verde → commit. Um commit por task.
- `gofmt -l .` vazio, `go vet ./...`, `golangci-lint run` (v2.13.2), `go test ./... -count=1`, `go build ./...`.
- Testes Postgres: `WZAP_TEST_DATABASE_URL` com banco sufixo `_test` + `postgrestest` (schema isolado por teste) + `postgres.Migrate`.
- Migrações: arquivo novo `internal/storage/migrations/00003_chatwoot.sql` (00002 reservado ao produto; confirmar numeração no apply); nunca editar aplicadas.
- Conexão: transições via `SetConnectionState`, nunca `Update` com linha lida.
- Produtor de eventos: só `events.Writer` (outbox); nenhum segundo relay; consumer durável é só leitura.
- Uma réplica: locks em memória ok; shutdown HTTP → outbox → cleaner → relay; worker do chatwoot para antes do relay.
- Referência Evolution é estudo: recriar, nunca copiar-colar.
- Termos canônicos: "Chatwoot account", "conversa operacional" (contato `123456` = detalhe), capability `wzap-chatwoot-inbound`, bot em pt-BR fixo.

---

## File Structure

**Criar:**
- `internal/storage/migrations/00003_chatwoot.sql` — `chatwoot_configs`, `chatwoot_messages` (+ índice `instance_id`, FK cascade no delete da instância).
- `internal/model/model.go` (+ structs, via modify) — `ChatwootConfig`, `ChatwootMessage`.
- `internal/storage/postgres/chatwoot.go` (+ `chatwoot_test.go`) — repos PG.
- `internal/chatwoot/config/config.go` (+ test) — validação de `set` (obrigatórios, URL, signMsg estrito, defaults).
- `internal/chatwoot/client/client.go` (+ test com `httptest`) — HTTP mínimo: contacts filter/search/create/update, inboxes list/create, conversations create/get/list/toggle, messages create/delete + upload multipart, contact_merge, `update_last_seen`.
- `internal/chatwoot/contacts/contacts.go` (+ test) — find/create/update, variantes BR, merge, grupos.
- `internal/chatwoot/conversations/conversations.go` + `locker.go` (+ tests) — cache TTL 30min + locker por `(instance, remoteJID)` (espelha `instancelock`, chaves string), reuso open/pending/reopen, expiração e validação.
- `internal/chatwoot/mapper/content.go` (+ test) — tipos WA→texto Chatwoot + markdown ida/volta (tabelas puras, sem I/O).
- `internal/chatwoot/mirror/worker.go` (+ test) — consumer durável, filtros, montagem e envio, edit/delete/read, avisos da conversa operacional.
- `internal/chatwoot/inbound/webhook.go` (+ test) — parse, filtros, assinatura, anexos→`Enqueue`, quoted, delete reverso, template, mark-read, comandos.
- `internal/httpapi/chatwoot.go` (+ test) — `PUT /instances/{id}/chatwoot`, `GET ...`, `POST /chatwoot/webhook/{id}` (aberta).

**Modificar:**
- `internal/storage/repository.go` — `ChatwootConfigRepository`, `ChatwootMessageRepository` (+ sentinelas).
- `internal/config/config.go` (+ test) — `WZAP_CHATWOOT_ENABLED/_BOT_CONTACT/_MESSAGE_READ/_MESSAGE_DELETE` (URI do import fica no Plano 2).
- `internal/session/session.go` — `EventSink` +2 (`OnMessageEdit`, `OnMessageDelete`), `Session` +3 (`DeleteMessage`, `MarkRead`, `PairPhone`).
- `internal/session/whatsmeow/events.go` — `ProtocolMessage` (REVOKE/EDIT) no dispatch.
- `internal/session/whatsmeow/media.go` + `pairing.go` + `manager.go` — `MarkRead`, `DeleteMessage` (revoke), pairing-code, feed history-sync equivalente.
- `internal/session/sessiontest/fake.go` — novos métodos no fake.
- `internal/events/subjects.go` — `MessageEdit`, `MessageDelete`.
- `internal/app/` — publica edit/delete via outbox (sink estendido).
- `internal/httpapi/server.go` — registra rotas (webhook fora do auth).
- `cmd/wzap/main.go` — fia repos, worker, consumer, rotas.
- `go.mod`/`go.sum` — 2 deps novas.

---

### Task 1: Persistência (tabela, repos, config)

**Files:**
- Create: `internal/storage/migrations/00003_chatwoot.sql`
- Modify: `internal/model/model.go`, `internal/storage/repository.go`
- Create: `internal/storage/postgres/chatwoot.go`
- Test: `internal/storage/postgres/chatwoot_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: `postgres.Migrate`, `postgrestest.NewPool`, `config.Load` existent
- Produces: `storage.ChatwootConfigRepository` (`Get/Put/Delete`), `storage.ChatwootMessageRepository` (`Put/GetByWAKey/DeleteByInstance`), `config.Chatwoot{Enabled, BotContact, MessageRead, MessageDelete}`, `model.ChatwootConfig`, `model.ChatwootMessage`

- [ ] **Step 1: migration + modelos.** Escrever `00003_chatwoot.sql` (goose `+goose Up/Down`): `chatwoot_configs(instance_id uuid PK REFERENCES instances(id) ON DELETE CASCADE, enabled bool NOT NULL DEFAULT false, url text NOT NULL DEFAULT '', account_id text NOT NULL DEFAULT '', token text NOT NULL DEFAULT '', name_inbox text NOT NULL DEFAULT '', sign_msg bool NOT NULL DEFAULT false, sign_delimiter text NOT NULL DEFAULT '', reopen_conversation bool NOT NULL DEFAULT true, conversation_pending bool NOT NULL DEFAULT false, merge_brazil_contacts bool NOT NULL DEFAULT false, import_contacts bool NOT NULL DEFAULT false, import_messages bool NOT NULL DEFAULT false, days_limit int NOT NULL DEFAULT 0, auto_create bool NOT NULL DEFAULT false, organization text NOT NULL DEFAULT '', logo text NOT NULL DEFAULT '', ignore_jids text[] NOT NULL DEFAULT '{}', created_at/updated_at timestamptz)`; `chatwoot_messages(instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE, wa_key text NOT NULL, chatwoot_message_id bigint NOT NULL, conversation_id bigint NOT NULL, inbox_id bigint NOT NULL, contact_source_id text NOT NULL DEFAULT '', is_read bool NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(instance_id, wa_key))` + índice em `(instance_id, chatwoot_message_id)`. Down dropa na ordem inversa. Em `model.go`: structs espelho com `time.Time`. Em `repository.go`: as duas interfaces + métodos acima.
- [ ] **Step 2: teste de integração falhando.** Em `chatwoot_test.go`: `pool := postgrestest.NewPool(t)` + `postgres.Migrate(ctx, pool)`; cria instância mínima, `Put` config, `Get` confere campo a campo; `Put` mapping, `GetByWAKey` confere; `DeleteByInstance` zera. Run: `WZAP_TEST_DATABASE_URL='postgres://wzap:secret@127.0.0.1:5432/wzap_test?sslmode=disable' go test ./internal/storage/postgres/ -run TestChatwoot -count=1 -v`. Expected: FAIL (nada implementado).
- [ ] **Step 3: implementar repos.** `postgres/chatwoot.go`: `NewChatwootRepositories(pool)` com SQL parametrizado `pgx`, `ErrNotFound` de `storage` quando ausente. Run teste. Expected: PASS.
- [ ] **Step 4: envs + teste de config.** Em `config.go`: struct `Chatwoot` + parse (`ENABLED` bool default false; demais bool default false; ausente = zero). Teste válido/ausente/inválido (`WZAP_CHATWOOT_ENABLED=banana` → erro nomeando a var). Run: `go test ./internal/config/ -count=1`. Expected: PASS.
- [ ] **Step 5: Commit.** `git add` dos 5 arquivos; `git commit -m "feat(chatwoot): persistência de config e correlação de IDs"` (pt-BR).

### Task 2: Validação de `set` + cliente HTTP

**Files:**
- Create: `internal/chatwoot/config/config.go`, `internal/chatwoot/config/config_test.go`
- Create: `internal/chatwoot/client/client.go`, `internal/chatwoot/client/client_test.go`

**Interfaces:**
- Consumes: `model.ChatwootConfig`, `storage.ChatwootConfigRepository`
- Produces: `config.Validate(c model.ChatwootConfig) error` (com `ErrField`), `client.New(baseURL, token, accountID) *Client` + métodos `FindContactByPhone`, `SearchContacts`, `CreateContact`, `UpdateContact`, `ListInboxes`, `CreateInbox`, `ListContactConversations`, `GetConversation`, `CreateConversation`, `ToggleConversationStatus`, `CreateMessage`, `CreateMessageWithAttachment`, `DeleteMessage`, `MergeContacts`, `UpdateLastSeen` — todos `(ctx, ...) (result, error)` com structs de req/resp mínimas em `client/models.go`

- [ ] **Step 1: teste de validação falhando.** Casos: habilitado sem `url`/`account_id`/`token` → erro por campo; URL malformada → erro; `sign_msg` sempre bool (Go já garante; validar `sign_delimiter` default aplicado fora); desabilitado passa com campos vazios. Run: `go test ./internal/chatwoot/config/ -count=1 -v`. Expected: FAIL.
- [ ] **Step 2: implementar `Validate` + defaults (`DefaultInbox(instanceName)`, `DefaultDelimiter()`).** Run. Expected: PASS.
- [ ] **Step 3: teste de cliente falhando.** `httptest.Server` que valida método/path/header `api_access_token` e devolve JSON de conversa; `CreateMessage` deve POSTar e decodificar `id`. Run: `go test ./internal/chatwoot/client/ -count=1 -v`. Expected: FAIL.
- [ ] **Step 4: implementar cliente.** `net/http` stdlib, timeout 15s, `do()` central com status!=2xx → `*client.Error{Status, Body}`; multipart para anexos (`mime/multipart`, `attachments[]`, `content`, `message_type`, `content_attributes` JSON, `source_id`, `source_reply_id`). Run. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(chatwoot): validação de config e cliente HTTP"`.

### Task 3: Contatos e conversas

**Files:**
- Create: `internal/chatwoot/contacts/contacts.go`, `contacts_test.go`
- Create: `internal/chatwoot/conversations/conversations.go`, `locker.go`, tests

**Interfaces:**
- Consumes: `client.Client`
- Produces: `contacts.Resolve(ctx, phone string, isGroup bool, name, avatar, jid string) (*Contact, error)` (busca → cria/atualiza → merge BR); `conversations.Resolve(ctx, instanceID uuid.UUID, remoteJID string, contactID int64) (int64, error)` (cache+lock+reuso/criação)

- [ ] **Step 1: teste de variantes BR falhando.** Função pura `variants("+5511999999999")` → `["+5511999999999", "+551199999999"]` e inversa; payload de filter com OR sem `+`. Run. Expected: FAIL.
- [ ] **Step 2: implementar variantes + `Resolve` de contatos** (busca grupo por identifier; fone por filter; >1 aplica mais-longo + merge quando `merge_brazil_contacts` e `+55`; cria com `phone_number:+fone`/grupo com identifier; atualiza nome/avatar quando vazio/divergente; `422` com jid → busca por identifier). Run. Expected: PASS.
- [ ] **Step 3: teste de lock falhando.** `locker` por chave string: 20 goroutines na mesma chave convergem para 1 execução do `fn`; chaves distintas não se bloqueiam; expiração libera. Run. Expected: FAIL.
- [ ] **Step 4: implementar `locker` + `Resolve` de conversas** (cache 30min com validação via `GetConversation`; lock 30s/poll 300ms/timeout 5s + double-check; reuso open / `pending` se `conversation_pending` / reabre se `reopen_conversation`; cria com `status:pending` quando flag). Run. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat(chatwoot): contatos com regra BR e conversas com lock"`.

### Task 4: Extensão de sessão/whatsmeow

**Files:**
- Modify: `internal/session/session.go`, `internal/session/whatsmeow/events.go`, `media.go`, `pairing.go`, `manager.go`, `internal/session/sessiontest/fake.go`, `internal/events/subjects.go`, `internal/app/*.go`
- Test: `internal/session/whatsmeow/*_test.go`, `internal/app/*_test.go`

**Interfaces:**
- Consumes: lib whatsmeow pinada (verificar níveis de history-sync no build)
- Produces: `EventSink.OnMessageEdit/OnMessageDelete`, `Session.DeleteMessage(ctx, chatJID, messageID)`, `Session.MarkRead(ctx, chatJID, senderJID, messageID)`, `Session.PairPhone(ctx, number) (code string, err error)`, `subjects.MessageEdit/MessageDelete`, feed history-sync por instância

- [ ] **Step 1: teste de dispatch falhando.** Evento `ProtocolMessage` REVOKE/EDIT sintetizado → `OnMessageDelete/OnMessageEdit` chamado no sink fake com IDs corretos. Run. Expected: FAIL.
- [ ] **Step 2: implementar dispatch + sink + subjects + publicação no outbox via `app`.** Run testes. Expected: PASS.
- [ ] **Step 3: teste de Delete/MarkRead/PairPhone falhando** (fake registra chamadas; whatsmeow delega para `client.RevokeMessage/MarkRead/PairPhone` da lib — confirmar assinaturas exatas no build). Run. Expected: FAIL.
- [ ] **Step 4: implementar os 3 + regra LID condicional** (usa alt-JID se a lib expuser, senão JID + `warn`). Run. Expected: PASS.
- [ ] **Step 5: feed history-sync equivalente** (progresso + lotes + contatos em acumuladores por instância, consumidos no Plano 2). Teste com eventos sintetizados da lib. Run. Expected: PASS.
- [ ] **Step 6: Commit.** `git commit -m "feat(chatwoot): extensão de sessão para paridade"`.

### Task 5: Mapeador de conteúdo (puro)

**Files:**
- Create: `internal/chatwoot/mapper/content.go`, `content_test.go`

**Interfaces:**
- Consumes: nada (puro)
- Produces: `mapper.Text(in TextInput) string`, `mapper.MarkdownToChatwoot(s) string`, `mapper.MarkdownToWhatsApp(s) string`, `mapper.Location(lat,lng,name,addr) string`, `mapper.Contact(fn,tels) string`, `mapper.GroupPrefix(phone,name,body) string`, `mapper.AttachmentKind(mime, ext) (kind string, asDocument bool)`

- [ ] **Step 1: testes falhando** (tabela: itálico/bold/riscado ida e volta; localização com link Maps; grupo com/sem texto; áudio→audio, `.svg`→document). Run. Expected: FAIL.
- [ ] **Step 2: implementar tabelas puras.** Run. Expected: PASS.
- [ ] **Step 3: Commit.** `git commit -m "feat(chatwoot): mapeador de conteúdo puro"`.

### Task 6: Worker do espelho

**Files:**
- Create: `internal/chatwoot/mirror/worker.go`, `worker_test.go`
- Modify: `cmd/wzap/main.go` (fia consumer)

**Interfaces:**
- Consumes: `client`, `contacts`, `conversations`, `mapper`, `media.Storage`, repos, `events` (leitura JetStream)
- Produces: `mirror.New(deps) *Worker`, `(*Worker).Run(ctx)` (durable `wzap-chatwoot`), `(*Worker).HandleMessage/HandleEdit/HandleDelete/HandleRead/HandleConnection` (exportados p/ teste)

- [ ] **Step 1: teste de HandleMessage falhando** (fakes: resolve contato+conversa, `CreateMessage` recebe `source_id:WAID:<id>` + `content_attributes`; `ignore_jids`/`status@broadcast` pulam). Run. Expected: FAIL.
- [ ] **Step 2: implementar handlers** (texto via `CreateMessage`; mídia via bytes do `media.Storage` + upload multipart; `media_omitted` repassa; edit/delete/read; avisos da conversa operacional pt-BR com throttle 30s). Run. Expected: PASS.
- [ ] **Step 3: dedup + consumer.** `getExistingSourceIds`-equivalente local (tabela correlação) + assinatura durável; teste de reentrega idempotente. Run. Expected: PASS.
- [ ] **Step 4: Commit.** `git commit -m "feat(chatwoot): worker do espelho WA→Chatwoot"`.

### Task 7: Entrada (webhook) + REST + fiação final

**Files:**
- Create: `internal/chatwoot/inbound/webhook.go`, `webhook_test.go`, `internal/httpapi/chatwoot.go`, `chatwoot_test.go`
- Modify: `internal/httpapi/server.go`, `cmd/wzap/main.go`

**Interfaces:**
- Consumes: `message.Service.Enqueue`, `media.Storage`, `contacts/conversations`, repos, `auth.Scope` do produto
- Produces: handlers `PUT/GET /instances/{id}/chatwoot`, `POST /chatwoot/webhook/{id}`; `inbound.Handle(ctx, instanceID, payload) (status int, err error)`

- [ ] **Step 1: testes de webhook falhando** (eco `WAID:`→200 sem efeito; private→200; texto→`Enqueue` com assinatura; anexo→download fake→`Save`→`Enqueue` mídia; quoted via correlação; comandos `status/init/clearcache/disconnect` na conversa operacional). Run. Expected: FAIL.
- [ ] **Step 2: implementar `inbound` + handlers REST** (webhook fora do auth; set/find atrás do dual; `auto_create` + `webhook_url` na resposta; validação 422). Run. Expected: PASS.
- [ ] **Step 3: fiação `serve()` + shutdown** (worker para antes do relay). Run: `go build ./...` + boot local com `WZAP_CHATWOOT_ENABLED` on/off. Expected: PASS.
- [ ] **Step 4: gates.** `gofmt -l .` vazio; `go vet ./...`; `golangci-lint run`; `WZAP_TEST_DATABASE_URL=... go test ./... -count=1`. Expected: tudo verde.
- [ ] **Step 5: Commit.** `git commit -m "feat(chatwoot): entrada, REST e fiação"`.

## Self-Review

- Specs cobertas: config (T1+T7), mirror (T5+T6), inbound (T7), contacts (T3), inbound-events delta (T4+T6). Import fica no Plano 2.
- Sem placeholders: comandos e SQL exatos acima; assinaturas da lib whatsmeow a confirmar no build (única abertura declarada).
- Consistência: nomes (`Resolve`, `Handle*`, `Validate`, `New`) definidos uma vez e reusados.
