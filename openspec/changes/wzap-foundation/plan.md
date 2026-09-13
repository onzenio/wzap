# wzap Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Construir o serviço `wzap` — gateway WhatsApp interno multi-instância em Go, com API REST de comandos, eventos no NATS JetStream, outbox de envio e mídia com TTL.

**Architecture:** Serviço único modular (Abordagem A): binário Go com `net/http`, persistência Postgres (pgx + goose, incluindo sqlstore do whatsmeow), eventos via NATS JetStream com outbox transacional, envio assíncrono com outbox em Postgres, mídia em volume local. Uma réplica por desenho; interfaces de sessão isolam o whatsmeow.

**Tech Stack:** Go 1.26, `net/http`, `log/slog`, `github.com/jackc/pgx/v5`, `github.com/pressly/goose/v3`, `github.com/nats-io/nats.go`, `go.mau.fi/whatsmeow`, `github.com/google/uuid`.

**Spec:** `openspec/changes/wzap-foundation/` — `proposal.md`, `design.md` e `specs/wzap-{instances,outbound-messaging,inbound-events,media,operations}/spec.md` (autoridade). `tasks.md` é o contrato de escopo; este plano é o detalhe de execução.

## Global Constraints

- Trabalhe **somente** em `wzap/` (novo workspace). Não altere `backend/`, `frontend/`, `docs/research/` nem `openspec/`.
- Módulo Go `wzap`; Go 1.26; **`CGO_ENABLED=0`**; sem SQLite, sem Redis.
- Dependências permitidas: `pgx/v5`, `goose/v3` (como biblioteca), `nats.go`, `go.mau.fi/whatsmeow`, `google/uuid`, `golang.org/x/sync` (se necessário). Não adicionar framework HTTP, ORM, logger de terceiros ou lib de validação.
- HTTP com `net/http`; logs com `log/slog` JSON.
- API sob `/api/v1`; JSON `snake_case`; sucesso `{"data": ...}`; erro `{"error":{"code","message"}}`; propagar `X-Request-Id`.
- Autenticação: `Authorization: Bearer <WZAP_SERVICE_TOKEN>` com comparação constant-time; `401` sem detalhes. Exceto `/healthz` e `/readyz`.
- Idempotência de envio: `Idempotency-Key` opcional; TTL 24h; replay devolve a resposta original com `X-Idempotent-Replay: true`; `409` em andamento; `422` mesmo key/conteúdo diferente.
- Eventos: stream `WZAP`, subjects `wzap.instances.{id}.{connection|message|receipt|message.status}`, envelope `{event_id,event_version,type,instance_id,occurred_at,payload}`, `Nats-Msg-Id = event_id`, publicação via `event_outbox` (at-least-once).
- Banco `wzap`; migrations goose em `wzap/internal/storage/migrations/`; tabelas `instances`, `message_queue`, `idempotency_keys`, `contacts`, `media`, `event_outbox` (núcleo com nomes/shape do apime).
- Uma réplica por desenho; lock por instância em memória; recuperação de mensagens presas no boot.
- Commits convencionais com escopo `wzap` (`feat(wzap): ...`, `fix(wzap): ...`, `chore(wzap): ...`). **Nunca** commite `AGENTS.md`, `CONTEXT.md` ou qualquer arquivo fora de `wzap/` e do change folder.
- TDD: teste que falha primeiro; sem código antes do teste. `gofmt` obrigatório; `golangci-lint run` limpo.
- Nomes de código e JSON em inglês; comentários e mensagens de erro de operação podem ser em português curto. Não copiar código do apime sem registrar em `wzap/THIRD_PARTY_NOTICES.md`.

---

### Task 1: Workspace, config e licenças

**Files:**
- Create: `wzap/go.mod`, `wzap/cmd/wzap/main.go`, `wzap/internal/config/config.go`, `wzap/internal/config/config_test.go`, `wzap/internal/version/version.go`, `wzap/.golangci.yml`, `wzap/.dockerignore`, `wzap/THIRD_PARTY_NOTICES.md`

**Interfaces:**
- Produces: `config.Config` com campos `HTTPAddr string`, `PublicURL string`, `ServiceToken string`, `DatabaseURL string`, `NATSURL string`, `NATSStream string`, `EventRetentionDays int`, `DataDir string`, `MediaTTLSeconds int`, `MaxMediaBytes int64`, `OutboxWorkers int`, `Humanize bool`, `LogLevel string`, `LogFormat string`, `AutoMigrate bool`; `config.Load() (Config, error)`; `version.Version = "0.1.0"`.

- [ ] **Step 1: Criar módulo e binário mínimo**

```bash
cd wzap && go mod init wzap && go build ./...
```

`cmd/wzap/main.go` imprime a versão e sai 0 (subcomandos entram na Task 6).

- [ ] **Step 2: Escrever teste do parser de config**

`config_test.go` com `t.Setenv`: caso completo carrega valores; caso sem `WZAP_SERVICE_TOKEN` retorna erro contendo `WZAP_SERVICE_TOKEN`; caso sem `WZAP_DATABASE_URL`/`WZAP_NATS_URL` erra. Defaults: `HTTPAddr=:8080`, `NATSStream=WZAP`, `EventRetentionDays=7`, `DataDir=/data`, `MediaTTLSeconds=7200`, `MaxMediaBytes=16777216`, `OutboxWorkers=4`, `LogLevel=info`, `LogFormat=json`, `AutoMigrate=true`.

- [ ] **Step 3: Rodar e ver falhar**

Run: `go test ./internal/config/ -v` — Expected: FAIL (tipo inexistente).

- [ ] **Step 4: Implementar `config.Load`** com `os.Getenv`, parsing numérico e erro agregado para obrigatórias (`WZAP_SERVICE_TOKEN`, `WZAP_DATABASE_URL`, `WZAP_NATS_URL`).

- [ ] **Step 5: Rodar e ver passar** — `go test ./internal/config/ -v` e `go build ./...`.

- [ ] **Step 6: Arquivos de qualidade e licença** — `.golangci.yml` (linters `govet,staticcheck,errcheck,ineffassign,unused,gosimple`), `.dockerignore` (`bin/`, `.git`, `*.md` exceto notices), `THIRD_PARTY_NOTICES.md` citando `open-apime/apime` (MIT) e que trechos de idempotência, normalização de JID e humanização podem ser adaptados sob essa licença.

- [ ] **Step 7: Commit** — `git add wzap && git commit -m "chore(wzap): scaffold module with config and lint"`.

### Task 2: Migrations e pool Postgres

**Files:**
- Create: `wzap/internal/storage/migrations/00001_init.sql`, `wzap/internal/storage/postgres/pool.go`, `wzap/internal/storage/postgres/migrate.go`, `wzap/internal/storage/postgres/migrate_test.go`

**Interfaces:**
- Produces: `postgres.Connect(ctx, databaseURL string) (*pgxpool.Pool, error)`; `postgres.Migrate(ctx, pool *pgxpool.Pool) error` (goose com FS embutido).

- [ ] **Step 1: Escrever a migration completa** (SQL exato, uma transação por statement; `-- +goose Up`):

```sql
CREATE TABLE instances (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  external_ref text UNIQUE,
  status text NOT NULL DEFAULT 'disconnected',
  whatsapp_jid text,
  last_connected_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE message_queue (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  recipient text NOT NULL,
  type text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'queued',
  retries int NOT NULL DEFAULT 0,
  last_error text,
  whatsapp_id text,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  media_id uuid,
  next_attempt_at timestamptz,
  read_at timestamptz
);
CREATE INDEX message_queue_instance_status_idx ON message_queue (instance_id, status);
CREATE INDEX message_queue_created_idx ON message_queue (created_at);
CREATE INDEX message_queue_wa_id_idx ON message_queue (whatsapp_id);
CREATE TABLE idempotency_keys (
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL,
  request_hash text NOT NULL,
  status text NOT NULL,
  response_status int,
  response_body jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (instance_id, idempotency_key)
);
CREATE INDEX idempotency_keys_expires_idx ON idempotency_keys (expires_at);
CREATE TABLE contacts (
  phone text PRIMARY KEY,
  jid text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contacts_expires_idx ON contacts (expires_at);
CREATE TABLE media (
  id uuid PRIMARY KEY,
  instance_id uuid NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
  direction text NOT NULL,
  message_id text,
  mimetype text NOT NULL,
  filename text,
  size_bytes bigint NOT NULL,
  storage_path text NOT NULL,
  sha256 text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX media_expires_idx ON media (expires_at);
CREATE TABLE event_outbox (
  id uuid PRIMARY KEY,
  subject text NOT NULL,
  envelope jsonb NOT NULL,
  attempts int NOT NULL DEFAULT 0,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz
);
CREATE INDEX event_outbox_pending_idx ON event_outbox (created_at) WHERE published_at IS NULL;
```

- [ ] **Step 2: Teste de integração da migration** (skip se `WZAP_TEST_DATABASE_URL` ausente): cria schema limpo, roda `Migrate`, verifica as 6 tabelas via `information_schema.tables`, roda de novo (idempotente).

- [ ] **Step 3: Implementar `pool.go`** (`pgxpool.ParseConfig` com `MaxConns=10` e `pgxpool.NewWithConfig`) e `migrate.go` com `goose.SetBaseFS(embedMigrations)` + `goose.Up`.

- [ ] **Step 4: Rodar** — `go test ./internal/storage/postgres/ -v` (com `WZAP_TEST_DATABASE_URL` apontando para o Postgres do compose; sem a var, o teste faz skip e o build ainda é validado).

- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add postgres schema and migrations"`.

### Task 3: Repositórios de instância e mensagem

**Files:**
- Create: `wzap/internal/model/model.go`, `wzap/internal/storage/repository.go`, `wzap/internal/storage/postgres/instances.go`, `wzap/internal/storage/postgres/messages.go`, `wzap/internal/storage/postgres/instances_test.go`, `wzap/internal/storage/postgres/messages_test.go`

**Interfaces:**
- Produces (tipos em `model`): `Instance{ID uuid.UUID, Name, ExternalRef, Status, WhatsAppJID, LastError string; LastConnectedAt *time.Time; CreatedAt, UpdatedAt time.Time}`; `OutboundMessage{ID, InstanceID uuid.UUID; Type, RecipientJID string; Payload []byte; MediaID *uuid.UUID; Status, WhatsAppMessageID, LastError string; Attempts int; DeliveredAt, ReadAt *time.Time; CreatedAt, UpdatedAt time.Time}`. Os campos Go mapeiam as colunas com shape do apime: `RecipientJID`→`recipient`, `Attempts`→`retries`, `WhatsAppMessageID`→`whatsapp_id`.
- Produces (interfaces em `storage`): `InstanceRepository{Create,Get,GetByExternalRef,List(limit,cursor),Update,Delete}`; `MessageRepository{Create,Get,ListByInstance,ClaimQueued(limit),MarkSent,MarkFailed,MarkRetrying,UpdateReceipt,RequeueStuck}`.
- `ClaimQueued` usa `SELECT ... WHERE status='queued' AND (next_attempt_at IS NULL OR next_attempt_at <= now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT $1` dentro de transação e marca `sending` antes do commit.

- [ ] **Step 1: Testes de integração** para cada método com Postgres real (skip sem env): criação, busca, conflito de `external_ref`, transição `queued→sending` por `ClaimQueued` com concorrência (duas goroutines não pegam a mesma linha), `MarkSent`, `MarkFailed`, `UpdateReceipt`, `RequeueStuck` (linha `sending` com `updated_at` antigo volta a `queued`).

- [ ] **Step 2: Rodar e ver falhar** — `go test ./internal/storage/postgres/ -run 'Instance|Message' -v`.

- [ ] **Step 3: Implementar repositórios** com pgx, sem SQL dinâmico além de filtros de listagem; `List` por `created_at` desc com cursor de id.

- [ ] **Step 4: Rodar e ver passar**; `go build ./...`.

- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add instance and message repositories"`.

### Task 4: Repositórios de idempotência, JID cache e outbox de eventos

**Files:**
- Create: `wzap/internal/storage/postgres/idempotency.go`, `wzap/internal/storage/postgres/jidcache.go`, `wzap/internal/storage/postgres/events.go`, tests correspondentes.

**Interfaces:**
- `IdempotencyRepository{Acquire(ctx, instanceID, key, fingerprint, expiresAt) (record *model.IdempotencyRecord, acquired bool, err error); Complete(ctx, instanceID, key, status int, body []byte) error; Release(ctx, instanceID, key) error; DeleteExpired(ctx) (int64, error)}` — `Acquire` com `INSERT ... ON CONFLICT DO NOTHING` + `SELECT`; conflito com mesmo fingerprint e `completed` retorna o registro; fingerprint diferente retorna erro sentinel `storage.ErrFingerprintMismatch`; em andamento retorna `storage.ErrInProgress`.
- `JIDCacheRepository{Get(ctx, phone) (jid string, ok bool, err error); Put(ctx, phone, jid, expiresAt) error; DeleteExpired(ctx) (int64, error)}` (tabela `contacts`, colunas `phone`/`jid`/`expires_at`).
- `EventOutboxRepository{Enqueue(ctx, id, subject, envelope []byte) error; ClaimPending(ctx, limit) ([]model.OutboxEvent, error); MarkPublished(ctx, id) error; MarkAttempt(ctx, id, errMsg) error; DeletePublishedBefore(ctx, t) (int64, error)}`.

- [ ] **Step 1: Testes de integração** cobrindo corrida de `Acquire` (duas goroutines, só uma adquire), replay, mismatch, expiração; `ClaimPending` concorrente; `MarkPublished`.

- [ ] **Step 2: Falhar → implementar → passar** (mesmo ciclo da Task 3).

- [ ] **Step 3: Commit** — `git commit -am "feat(wzap): add idempotency, jid cache and event outbox repositories"`.

### Task 5: HTTP core (server, envelope, request-id, auth)

**Files:**
- Create: `wzap/internal/httpapi/server.go`, `middleware.go`, `response.go`, `server_test.go`, `middleware_test.go`.

**Interfaces:**
- Produces: `httpapi.New(cfg config.Config, log *slog.Logger, deps Deps) *http.Server`; `Deps` struct com interfaces das Tasks seguintes (instância, mensagem, mídia, número, idempotência). `response.JSON(w, status, data)`; `response.Error(w, r, status, code, message)`; middleware `RequestID`, `Logging`, `Recover`, `Auth(token)`.

- [ ] **Step 1: Testes de middleware** — `Auth`: sem header → 401 `{"error":{"code":"unauthorized"}}`; token errado → 401; token certo → 200 e `X-Request-Id` presente/ecoado; `Recover` transforma panic em 500 sem vazar stack.

- [ ] **Step 2: Falhar → implementar → passar.** `New` registra `GET /healthz` e `GET /readyz` (handlers entram na Task 6; por ora stubs 200) e monta o grupo autenticado `/api/v1` vazio.

- [ ] **Step 3: Commit** — `git commit -am "feat(wzap): add http server core with auth and request id"`.

### Task 6: Health, readiness, subcomandos e encerramento

**Files:**
- Create: `wzap/internal/httpapi/health.go`, `health_test.go`; Modify: `wzap/cmd/wzap/main.go`.

**Interfaces:**
- Produces: `health.Checker` com `Check(ctx) error` agregando `Ping(ctx)` do Postgres e o estado das migrações (a checagem do broker é adicionada na Task 7). `/healthz` sempre 200 `{"data":{"status":"ok"}}`; `/readyz` 200 ou 503 `{"data":{"status":"ready|unready","checks":{...}}}`.
- `cmd/wzap` aceita subcomandos: `serve` (default), `migrate`, `healthcheck` (GET em `http://127.0.0.1$WZAP_HTTP_ADDR/readyz`, exit 0/1).

- [ ] **Step 1: Teste de readiness** com fakes: dependência ok → 200; dependência falha → 503.
- [ ] **Step 2: Falhar → implementar → passar.**
- [ ] **Step 3: Encerramento gracioso** em `main.go`: `signal.NotifyContext(SIGINT,SIGTERM)`, `server.Shutdown` com timeout 10s e parada dos workers.
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add health, subcommands and graceful shutdown"`.

### Task 7: Núcleo de eventos (envelope, stream e relay do outbox)

**Files:**
- Create: `wzap/internal/events/envelope.go`, `envelope_test.go`, `subjects.go`, `publisher.go`, `relay.go`, `relay_test.go`

**Interfaces:**
- Produces: `events.Envelope{EventID uuid.UUID; EventVersion int; Type string; InstanceID uuid.UUID; OccurredAt time.Time; Payload json.RawMessage}`; `events.New(type string, instanceID uuid.UUID, payload any) (Envelope, error)` (serializa payload, `EventVersion=1`); `events.Subjects.Connection(instanceID)`, `.Message`, `.Receipt`, `.MessageStatus` (strings `wzap.instances.<id>.connection` etc.).
- Produces: `events.Publisher` interface `{EnsureStream(ctx) error; Publish(ctx, subject string, env Envelope) error}`; `events.Writer` interface `{Write(ctx, subject string, env Envelope) error}` (grava no outbox); `events.Relay{outbox, publisher, log, retentionDays}` com `Run(ctx)` e `PublishNow(ctx, pending)`.
- Envelope JSON: `{"event_id","event_version","type","instance_id","occurred_at","payload"}`; `occurred_at` RFC3339Nano UTC.

- [ ] **Step 1: Testes unitários do envelope** — campos serializados, `event_id` único, payload aninhado preservado, rejeição de payload não serializável.
- [ ] **Step 2: Testes do relay** com outbox fake + publisher fake: publica pendentes, marca `published_at`, em falha incrementa `attempts` e mantém pendente, retry depois com backoff, limpa publicados antigos.
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Publisher NATS** — `EnsureStream` cria/atualiza stream `WZAP` com subjects `wzap.>` e `MaxAge=retentionDays`, tolerando "já existe"; `Publish` com `nats.MsgId(env.EventID.String())`.
- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add event envelope, stream and outbox relay"`.

### Task 8: Sessão (interface, fake e manager whatsmeow)

**Files:**
- Create: `wzap/internal/session/session.go`, `wzap/internal/session/sessiontest/fake.go`, `wzap/internal/session/whatsmeow/manager.go`, `pairing.go`, `events.go`, `store.go`, `manager_test.go`

**Interfaces:**
- Produces: `session.Status` (`disconnected`, `pairing`, `connected`, `error`); `session.OutboundMessage{Type string; RecipientJID string; Payload []byte; MediaPath string}`; `session.EventSink{OnMessage(ctx, InboundMessage); OnReceipt(ctx, Receipt); OnConnection(ctx, instanceID uuid.UUID, status Status, jid string, reason string)}`; `session.InboundMessage{InstanceID; MessageID; ChatJID; SenderJID; IsGroup bool; Type string; Text string; MediaToolkit...}` — mídia entra na Task 17 (aqui only struct with `MediaAvailable bool`, `MediaMime string`, `MediaFilename string`, `MediaDownload func(ctx) ([]byte, error)`).
- Produces: `session.Manager` interface `{RestoreAll(ctx) error; Get(instanceID uuid.UUID) (Session, bool); Create(instance *model.Instance) (Session, error); Remove(ctx, instanceID) error}`; `Session` interface `{Connect(ctx) (qr string, expiresAt time.Time, err error); QR(ctx) (string, time.Time, error); Send(ctx, msg OutboundMessage) (whatsappID string, err error); IsOnWhatsApp(ctx, phone string) (jid string, ok bool, err error); SendPresence(ctx, chatJID, state string) error; Disconnect(ctx) error; Status() Status; JID() string}`.
- Fake em `sessiontest`: implementa Manager/Session em memória, registra chamadas, permite forçar erro e disparar eventos via `sink`.

- [ ] **Step 1: Teste do fake** garante que ele satisfaz as interfaces (compile-time `var _ session.Manager = (*Fake)(nil)`) e que eventos são emitidos para o sink.
- [ ] **Step 2: Implementar `manager.go`** com `sqlstore` (`pgx`) e logger adaptado a `slog`; devices por instância; `Create` retorna sessão com device novo; `RestoreAll` com concorrência limitada (semáforo 2) e jitter.
- [ ] **Step 3: `pairing.go`** — abrir canal de QR (`GetQRChannel`), `Connect`, monitorar `code`/`timeout`/`success`; em `success` registrar JID e emitir `OnConnection(connected)`.
- [ ] **Step 4: `events.go`** — traduzir eventos whatsmeow para `EventSink` (mensagens, recibos, desconexão/restrição) mantendo tipos da lib confinados ao pacote.
- [ ] **Step 5: `go build ./...` e `go test ./internal/session/...`** (manager não é testável sem dispositivo; cobrir com fake e compile-check).
- [ ] **Step 6: Commit** — `git commit -am "feat(wzap): add whatsmeow session manager behind interfaces"`.

### Task 9: Serviço de instâncias e CRUD REST

**Files:**
- Create: `wzap/internal/instance/service.go`, `service_test.go`, `wzap/internal/httpapi/instances.go`, `instances_test.go`

**Interfaces:**
- Produces: `instance.Service` com `Create(ctx, CreateInput{Name, ExternalRef string})`, `Get(ctx, id)`, `List(ctx, limit int, cursor string)`, `Update(ctx, id, UpdateInput)`, `Delete(ctx, id)`; erros `instance.ErrNotFound`, `instance.ErrExternalRefTaken`.
- REST (contrato): `POST /api/v1/instances` → 201; `GET /api/v1/instances` → 200 `{items,next_cursor}`; `GET/PATCH/DELETE /api/v1/instances/{id}` → 200/200/204; `409` para ref duplicada; `404` inexistente.

- [ ] **Step 1: Testes do serviço** com repositório fake: criação gera UUID e status `disconnected`; ref duplicada → `ErrExternalRefTaken`; update parcial; delete remove sessão (fake manager) e mídias (fake storage).
- [ ] **Step 2: Testes dos handlers** com `httptest` + service fake: status codes e envelope de erro.
- [ ] **Step 3: Falhar → implementar → passar** — `go test ./internal/instance/ ./internal/httpapi/`.
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add instance service and rest crud"`.

### Task 10: Pareamento por QR e eventos de conexão

**Files:**
- Create: `wzap/internal/app/runtime.go`, `wzap/internal/httpapi/connection.go`; Modify: `internal/instance/service.go`, `cmd/wzap/main.go`

**Interfaces:**
- Produces: `instance.Service.Connect(ctx, id) (ConnectResult{Status model.Status; QRCode string; QRExpiresAt *time.Time}, error)` — se já `connected`, retorna status sem QR; senão cria sessão e devolve QR com validade; persiste `pairing`.
- Produces: `app.Runtime` implementando `session.EventSink`: em `OnConnection` atualiza `instances.status`/`whatsapp_jid`/`last_error` e grava evento `connection` no outbox.
- REST: `POST /api/v1/instances/{id}/connect` → 200; `GET /api/v1/instances/{id}/qr` → 200 QR ou 409 se conectada; `GET /api/v1/instances/{id}/status` → 200.

- [ ] **Step 1: Testes** com fake manager: conectar instância desconectada devolve QR e status `pairing`; conectar já conectada devolve `connected` sem QR; status reflete mudanças feitas pelo sink.
- [ ] **Step 2: Falhar → implementar → passar.**
- [ ] **Step 3: Ligar o `Runtime` no `main.go`** (sink do manager) e verificar `go build ./...`.
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add qr pairing and connection events"`.

### Task 11: Reconexão, desconexão, restrição, restauração e remoção

**Files:**
- Modify: `wzap/internal/session/whatsmeow/events.go`, `manager.go`, `internal/instance/service.go`, `internal/httpapi/connection.go`; tests correspondentes.

**Interfaces:**
- `POST /api/v1/instances/{id}/disconnect` → 204: chama `Session.Disconnect`, limpa JID/status e grava evento `connection`.
- `OnConnection(disconnected)` por queda transitória dispara reconexão com backoff exponencial (base 2s, teto 2min, jitter); `OnConnection(error)` por logout/restrição não reconecta.
- `Manager.RestoreAll` restaura devices persistidos no boot; falha vira `error` com motivo.
- `Delete` limpa sessão, mídias (arquivos + linhas) e linhas relacionadas.

- [ ] **Step 1: Testes de transição** no fake/manager com sprinkles de tempo controlado (`clock` injetável): queda → tentativas com espera crescente; logout → `disconnected` sem nova tentativa; restrição → `error`.
- [ ] **Step 2: Falhar → implementar → passar.**
- [ ] **Step 3: Teste de restauração** no service com manager fake: boot chama `RestoreAll` e reflete estado.
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add reconnect, restore and lifecycle transitions"`.

### Task 12: Resolver de JID (9º dígito BR) e endpoint de checagem

**Files:**
- Create: `wzap/internal/message/jidresolver.go`, `jidresolver_test.go`, `wzap/internal/httpapi/numbers.go`, `numbers_test.go`

**Interfaces:**
- Produces: `message.JIDResolver{Resolve(ctx, instanceID uuid.UUID, phone string) (jid string, err error)}`; erros `message.ErrNumberNotFound`, `message.ErrResolverUnavailable`; usa `JIDCacheRepository` (positivo) e cache negativo em memória com TTL curto; chama `Session` (`IsOnWhatsApp`).
- Regra BR: normalizar para dígitos; DDD + 9 dígitos (13 com 55) tenta variante sem o 9º; DDD + 8 dígitos tenta variante com 9º apenas se o primeiro dígito for 6-9; prefixo fixo (2-5) não tenta variante; JID que já contém `@` passa direto.
- REST: `POST /api/v1/instances/{id}/numbers/check` → 200 `{exists, jid, normalized}`; inexistente `exists:false`.

- [ ] **Step 1: Testes table-driven da normalização** (casos acima, com/sem `55`, com/sem 9º, fixo, inválido, JID pronto) e do cache (positivo evita segunda consulta; negativo respeita TTL).
- [ ] **Step 2: Falhar → implementar → passar.**
- [ ] **Step 3: Handler + teste de contrato** (200 com existente, 404/422 para instância inexistente).
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add brazilian jid resolver and number check"`.

### Task 13: Idempotência de envio e aceite de mensagens

**Files:**
- Create: `wzap/internal/httpapi/idempotency.go`, `idempotency_test.go`, `wzap/internal/message/service.go`, `service_test.go`, `wzap/internal/httpapi/messages.go`, `messages_test.go`; Modify: `internal/app/runtime.go` (enqueue de eventos)

**Interfaces:**
- Produces: middleware `Idempotency(repo, log)` envolvendo apenas os POSTs de envio: primeiro request adquire (`in_progress`), captura status+body, completa (TTL 24h); replay repete a resposta com `X-Idempotent-Replay: true`; em andamento → `409`; fingerprint diferente → `422`; 4xx libera a key; 5xx mantém; multipart usa fingerprint de método+rota+campos de texto (arquivo não entra no hash — limitação documentada).
- Produces: `message.Service{Enqueue(ctx, instanceID uuid.UUID, input EnqueueInput) (uuid.UUID, error)}` com `EnqueueInput{Type, To, Text, Caption, Filename, PTT bool, Latitude, Longitude float64, DisplayName, VCard string, MediaID *uuid.UUID}`; valida instância conectada (`ErrInstanceNotConnected` → 409), resolve JID, insere `message_queue(queued)`, retorna id.
- REST: `POST /api/v1/instances/{id}/messages/{text,location,contact,media}` → 202 `{message_id,status:"queued"}`; `GET /api/v1/instances/{id}/messages/{message_id}` → 200; `GET /api/v1/instances/{id}/messages` → 200 paginado.

- [ ] **Step 1: Testes unitários do middleware** com repo fake: sem key passa direto; primeiro request completa e grava; replay devolve body e header; in-flight 409; mismatch 422; 4xx libera key.
- [ ] **Step 2: Testes do serviço** com fakes: instância desconectada 409; número inválido → 422; sucesso grava `queued` com payload normalizado.
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Teste de handler** de consulta de status e listagem.
- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add idempotent send acceptance and message queries"`.

### Task 14: Workers do outbox, envio e recuperação

**Files:**
- Create: `wzap/internal/instancelock/locker.go`, `wzap/internal/instancelock/locker_test.go`, `wzap/internal/message/outbox.go`, `outbox_test.go`, `senders.go`, `senders_test.go`; Modify: `internal/message/service.go`, `cmd/wzap/main.go`

**Interfaces:**
- Produces: `message.Outbox{repo, manager, writer, log, workers int, lock *instance.Locker}` com `Run(ctx)`; `StartRecovery` chamando `RequeueStuck` no boot (threshold 5min).
- `message.Sender` interface `{Send(ctx, sess session.Session, msg session.OutboundMessage) (string, error)}`; implementações `textSender`, `locationSender`, `contactSender` (dispatched por `Type`).
- Erros de sessão: `session.ErrTransient` (retry), `session.ErrNotConnected` (falha definitiva, status `failed` — a spec não tem estado `error` de mensagem), `session.ErrInvalidRecipient` (falha definitiva).
- `ClaimQueued` respeita `next_attempt_at`; falha transitória atualiza `next_attempt_at = now + 2^retries segundos` (teto 2min) e volta `queued`; `retries >= 5` → `failed`.
- Evento `message.status` gravado no outbox em `sent` e `failed`.

- [ ] **Step 1: Testes do worker** com repo/manager fakes: processa lote, marca `sending→sent`; erro transitório reagenda com `next_attempt_at` crescente; após 5 tentativas vira `failed`; instância desconectada falha definitivo; recuperação devolve `sending` antigo para `queued`.
- [ ] **Step 2: Testes dos senders (texto/localização/contato)** com sessão fake: monta `OutboundMessage` correto por tipo (sem `quoted` — fora do escopo da v1).
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Ligar `Outbox` no `main.go`** (workers da config) e verificar `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add outbox workers, senders and recovery"`.

### Task 15: Recibos e humanização

**Files:**
- Create: `wzap/internal/message/receipts.go`, `receipts_test.go`, `humanize.go`, `humanize_test.go`; Modify: `internal/app/runtime.go`, `internal/session/session.go` (adicionar `SendPresence`)

**Interfaces:**
- Produces: `message.Receipts{repo, writer}.Apply(ctx, r session.Receipt) error` — `delivered`/`read`/`played` atualizam `delivered_at`/`read_at` e gravam evento `receipt` correlacionando `whatsapp_id`.
- Produces: `message.Humanizer{Sleep func(ctx, d); Rand}` com `PresenceFor(msgType, textLen, mediaBytes int, firstContact bool) time.Duration` (texto ~40ms/char com teto 8s; áudio teto 15s; mídia por tamanho; primeiro contato +1.5–2.5s) e `BeforeSend(ctx, sess, chatJID, d)` enviando presença `composing`/`paused`; `Enabled=false` não dorme.
- `session.Session` ganha `SendPresence(ctx, chatJID, state string) error`.

- [ ] **Step 1: Testes table-driven da math de humanização** (independentes de relógio; `Sleep` injetado) — casos de texto curto/longo, áudio, mídia, primeiro contato, desabilitado.
- [ ] **Step 2: Testes de recibos** com repo fake: lido atualiza `read_at` e gera evento; recibo de mensagem alheia é ignorado.
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Ligar no `Runtime.OnReceipt` e no outbox (BeforeSend quando `Humanize`)**; `go build ./...`.
- [ ] **Step 5: Commit** — `git commit -am "feat(wzap): add receipts handling and optional humanization"`.

### Task 16: Núcleo de mídia e download autenticado

**Files:**
- Create: `wzap/internal/media/storage.go`, `storage_test.go`, `cleaner.go`, `cleaner_test.go`, `wzap/internal/httpapi/media.go`, `media_test.go`

**Interfaces:**
- Produces: `media.Storage{Save(ctx, instanceID uuid.UUID, direction, messageID, mimetype, filename string, data []byte) (*model.Media, error); Open(ctx, id uuid.UUID) (io.ReadCloser, *model.Media, error); DeleteByInstance(ctx, instanceID uuid.UUID) error; DeleteExpired(ctx, now time.Time) (int, error)}` — grava em `DataDir/media/<instance_id>/<media_id>`, calcula sha256, rejeita vazio/acima do limite.
- Produces: `media.Cleaner{storage, interval}` com `Run(ctx)` removendo expirados; `media.AllowedMime(mimetype) bool` e `media.MaxBytes` da config.
- REST: `GET /api/v1/media/{id}` → 200 com `Content-Type` e `Content-Length`; `401` sem token; `404` inexistente/expirada.

- [ ] **Step 1: Testes de storage** em diretório temporário: salva e lê conteúdo idêntico, checksum confere, `DeleteExpired` remove arquivo e linha, `DeleteByInstance` limpa tudo da instância.
- [ ] **Step 2: Testes do handler** com storage fake: 200 com content-type, 401 sem auth, 404 expirada.
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add media storage, cleaner and authenticated download"`.

### Task 17: Inbound (mensagens e mídia) e eventos

**Files:**
- Create: `wzap/internal/app/inbound.go`, `inbound_test.go`; Modify: `internal/app/runtime.go`

**Interfaces:**
- Produces: `Runtime.OnMessage(ctx, session.InboundMessage)` (void — a interface `session.EventSink` é a autoridade) — com mídia e dentro do limite, baixa e salva (`direction=inbound`); acima do limite (pré-check por `MediaLength` + cap no stream) marca `media_omitted`; grava evento `message` no outbox com payload da spec (`from_jid`, `chat_jid`, `is_group`, `message_id`, `timestamp`, `type`, `text`, `media{media_id,mimetype,filename,size,url}` — sem `reply_to`, descopado da v1), URL `PublicURL + /api/v1/media/{id}`.
- `Runtime.OnConnection` (já existente) publica `connection`; `OnReceipt` publica `receipt`.

- [ ] **Step 1: Testes** com sessão/storage fakes: texto gera payload com campos obrigatórios; mídia dentro do limite é salva e referenciada com URL e `expires_at`; mídia acima do limite gera `media_omitted`; falha de download não derruba o handler e registra `last_error` no evento? (não: gera `media_omitted` com motivo).
- [ ] **Step 2: Falhar → implementar → passar.**
- [ ] **Step 3: Teste de integração do envelope** contra a spec (`event_version`, tipo, subject).
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): handle inbound messages and publish events"`.

### Task 18: Envio de mídia

**Files:**
- Create: `wzap/internal/message/senders_media.go`, `senders_media_test.go`; Modify: `internal/message/senders.go`, `internal/httpapi/messages.go`

**Interfaces:**
- `POST /api/v1/instances/{id}/messages/media` (multipart: `file`, `to`, `type`, `caption`, `ptt`, `filename`) → valida mime/tamanho, salva `media` (`direction=outbound`), enfileira com `media_id`; inválido → 422 sem enfileirar.
- `mediaSender` no outbox abre o arquivo pelo `media.Storage`, monta `session.OutboundMessage{MediaPath,...}` e envia; tipo determinado por `type`/mime (imagem, vídeo, áudio/PTT, documento).

- [ ] **Step 1: Testes do handler multipart** com storage fake: 202 com `media_id` salvo; mime inválido 422; arquivo acima do limite 422; sem arquivo 422.
- [ ] **Step 2: Testes do sender** com sessão fake: mídia existente vira `OutboundMessage` com `MediaPath`; mídia expirada falha definitivo.
- [ ] **Step 3: Falhar → implementar → passar.**
- [ ] **Step 4: Commit** — `git commit -am "feat(wzap): add media upload and outbound media sending"`.

### Task 19: Empacotamento, compose e CI

**Files:**
- Create: `wzap/Dockerfile`, `wzap/docker/postgres-init.sql`, `.github/workflows/wzap.yml` (raiz do repo, no worktree); Modify: `docker-compose.yml` (raiz)

**Interfaces:**
- Dockerfile multi-stage: builder `golang:1.26` com `CGO_ENABLED=0 go build -o /out/wzap ./cmd/wzap`; final `gcr.io/distroless/static-debian12:nonroot` copiando `/out/wzap`, `EXPOSE 8080`, `ENTRYPOINT ["/wzap"]`, `CMD ["serve"]`, `HEALTHCHECK` via `/wzap healthcheck`.
- Compose: serviço `wzap` (`build: ./wzap`, `127.0.0.1:8081:8080`, env `WZAP_SERVICE_TOKEN`, `WZAP_DATABASE_URL=postgres://wzap:secret@postgres:5432/wzap?sslmode=disable`, `WZAP_NATS_URL=nats://nats:4222`, `WZAP_AUTO_MIGRATE=true`, `WZAP_DATA_DIR=/data`, volume `wzap-media:/data`, `depends_on` postgres/nats `service_healthy`, `restart: unless-stopped`).
- `postgres-init.sql`: `CREATE DATABASE wzap;` montado em `docker-entrypoint-initdb.d` do serviço postgres (vale para volumes novos).
- CI: `.github/workflows/wzap.yml` com `paths: [wzap/**]`, setup-go 1.26, cache, `golangci-lint`, `go test ./...`, `go build ./...`, com serviço Postgres para os testes de integração.

- [ ] **Step 1: Build da imagem** — `docker build -t wzap:dev wzap` e `docker run --rm wzap:dev healthcheck` falha controladamente sem config? (esperado: erro de config; valida o binário).
- [ ] **Step 2: Subir o serviço no compose** — `docker compose up -d wzap` e verificar `/readyz` em `127.0.0.1:8081`.
- [ ] **Step 3: Banco no volume atual** — verificar com `docker compose exec postgres psql -U onefisc -tAc "SELECT datname FROM pg_database WHERE datname='wzap'"`; se ausente, criar com `createdb` uma vez (registrar no README) e confirmar migrações aplicadas.
- [ ] **Step 4: Workflow de CI** válido (`actionlint` ou revisão) e verde em execução local equivalente (`go test`, `golangci-lint`).
- [ ] **Step 5: Commit** — `git commit -am "chore(wzap): add docker image, compose service and ci"`.

### Task 20: README e verificação final

**Files:**
- Create: `wzap/README.md`; Modify: `openspec/changes/wzap-foundation/tasks.md` (marcar concluídas)

**Interfaces:**
- README: visão, requisitos, configuração completa (`WZAP_*`), contrato REST resumido, subjects/envelope de eventos, operação local (`docker compose up`), `createdb`, testes e checklist manual de pareamento/envio real.

- [ ] **Step 1: Escrever o README** conforme acima.
- [ ] **Step 2: Verificação local completa** — `docker compose up -d` (stack), `curl 127.0.0.1:8081/readyz`, criar instância via curl, iniciar pareamento e obter QR; registrar a saída.
- [ ] **Step 3: Suíte e lint** — `cd wzap && go test ./... && golangci-lint run && go build ./...`; registrar saída limpa.
- [ ] **Step 4: Atualizar `tasks.md`** do change marcando o que foi concluído (8.1–8.3; 8.4 manual fica pendente do humano).
- [ ] **Step 5: Commit** — `git commit -am "docs(wzap): add readme and final verification notes"`.

## Self-Review

**Cobertura das specs:**

- `wzap-instances`: Tasks 8–11 (CRUD, QR, reconexão, restrição, restauração, remoção).
- `wzap-outbound-messaging`: Tasks 12–15 e 18 (JID, idempotência, aceite, outbox, retries, status, recibos, humanização, mídia).
- `wzap-inbound-events`: Tasks 7, 10, 15, 17 (envelope, stream, relay, connection/message/receipt/status).
- `wzap-media`: Tasks 16 e 18 (storage, TTL, download autenticado, upload, identidade opaca).
- `wzap-operations`: Tasks 1–6 e 19–20 (config, auth, health, migrações, empacotamento, execução local, observabilidade, shutdown).

**Placeholders:** nenhum TBD/TODO; todos os passos têm comando, código ou comportamento observável.

**Consistência de tipos:** `session.Manager/Session` definidos na Task 8 e consumidos nas Tasks 10–18; `storage.*Repository` definidos nas Tasks 3–4 e usados nas seguintes; `events.Envelope/Writer` definidos na Task 7 e usados em 10, 15, 17; `model.Media` definido na Task 16 e referenciado por `EnvelopeInput.MediaID` (Task 13) e envio de mídia (Task 18).

