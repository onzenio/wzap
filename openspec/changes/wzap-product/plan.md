# Plan — wzap-product (por task ID)

Detalhe de implementação do `tasks.md`. Ordem de execução: grupo 1 → 2 →
3 → 4 → 5 → 6 → 7 (cada grupo só começa com o anterior verde).
Padrão em todo grupo: TDD (teste falhando primeiro), `gofmt -l .` vazio,
`go vet ./...`, `golangci-lint run`, `go test ./... -count=1` (+ Postgres com
`WZAP_TEST_DATABASE_URL` quando tocar storage), `go build ./...`.
Testes Postgres usam `internal/storage/postgres/postgrestest` (schema isolado,
nunca o schema compartilhado). Recriar a partir das referências, nunca
copiar-colar (sem notices novos). Novas deps Go: `github.com/golang-jwt/jwt/v5`
(JWT); `golang.org/x/crypto` (bcrypt — já transitiva, promover a direta).

## 1. Persistência e configuração

- **1.1** Criar `internal/storage/migrations/00002_product.sql` (goose, embutido
  via `embed.go` sem alteração): tabela `users` (`id uuid PK`, `email text`,
  `password_hash text`, `role text CHECK IN ('admin','user')`,
  `instance_quota int NOT NULL DEFAULT 0`, timestamps; índice único em
  `lower(email)`); `ALTER TABLE instances ADD COLUMN owner_user_id uuid
  REFERENCES users(id)`, `api_key_hash text`, `webhook_url text`,
  `webhook_enabled boolean NOT NULL DEFAULT false`, `webhook_events text[]
  NOT NULL DEFAULT '{message,receipt,connection,message.status}'`.
  Coluna owner fica nulável aqui; o NOT NULL é lógico (backfill no seed, 2.2).
  Down reverte na ordem inversa. Verificação: teste em
  `internal/storage/postgres/migrate_test.go` aplica em banco limpo e em banco
  com `00001` + instâncias legadas.
- **1.2** Estender `internal/storage/repository.go`: sentinel `ErrEmailTaken` +
  interface `UserRepository` (`Create`, `GetByID`, `GetByEmail` case-insensitive,
  `List`, `Delete`, `Count`) e `APIKeyRepository` (`SetHash`, `InstanceByHash`,
  `ClearHash`, `CountByOwner`, `CountAll`). Implementar em
  `internal/storage/postgres/users.go` e `apikeys.go` (+ `*_test.go` com
  `postgrestest`). `model.go`: struct `User` e `Instance.OwnerUserID uuid.UUID`
  (+ `WebhookURL/Enabled/Events` lidos, sem hash exposto).
- **1.3** Em `internal/config/config.go`: remover `ServiceToken`/
  `WZAP_SERVICE_TOKEN`, adicionar `APIKey` (`WZAP_API_KEY`, obrigatória),
  `AdminEmail`/`AdminPassword` (`WZAP_ADMIN_*`, opcionais), `JWTSecret`
  (`WZAP_JWT_SECRET`, obrigatória), `MaxInstances` (`WZAP_MAX_INSTANCES`,
  default 0 = ilimitado, permite 0 — helper novo `nonNegativeIntValue`) e
  `DefaultUserQuota` (`WZAP_DEFAULT_USER_INSTANCE_QUOTA`, idem). Estender
  `config_test.go` (válido/ausente/inválido).

## 2. Autenticação e autorização

- **2.1** Novo pacote `internal/auth/auth.go`: `HashPassword`/`CheckPassword`
  (bcrypt), `MintAPIKey` (32 bytes `crypto/rand`, base64url; hash hex sha256),
  `MintToken`/`ParseToken` (JWT HS256, claims sub/role/exp curto), `Scope{Kind:
  global|user|instance, UserID, Role, InstanceID}` + helpers de contexto.
  Handlers em `internal/httpapi/auth.go`: `POST /auth/login` (bcrypt, cookie
  `wzap_session` HttpOnly + `SameSite=Lax` + `Secure` se `PublicURL` https),
  `POST /auth/logout` (limpa cookie), `GET /auth/me`. Testes: `200`/`401`
  (sem indicar campo), sessão invalidada pós-logout.
- **2.2** Seed em `cmd/wzap/main.go` (`serve`, antes de servir): se `users`
  vazia e `WZAP_ADMIN_*` configuradas → cria admin (bcrypt) + backfill
  `owner_user_id` das instâncias legadas para ele; sem envs → nada criado.
  Testes com tabela vazia/cheia, com/sem envs.
- **2.3** Substituir `Auth`/`validBearer` em `internal/httpapi/middleware.go`
  por `Authenticate(globalKey, users, keys, jwtSecret)`: ordem cookie de sessão
  → `apikey:` igual à global → sha256 lookup no banco; injeta `auth.Scope` no
  contexto; falha → `401 unauthorized`. Helpers `ScopeFromContext`,
  `RequireRole`, `RequireInstance`. Teste da matriz global/user/instância.
- **2.4** Aplicar RBAC+ownership em todas as rotas: admin tudo; user só as
  próprias (lista filtra por dono); key só a própria instância (fora → `403`,
  coleções gerais → `403`); inexistente → `404`. Atualizar `serve()` dos testes
  para header `apikey:`.
- **2.5** Em `internal/httpapi/server.go`: sub-mux da API montado em `/` atrás
  do `Authenticate`; públicas exatas `GET /healthz`, `GET /readyz`, `/swagger/`,
  `/manager/` (+ estáticos). Rotas sem prefixo (`/instances`,
  `/instances/{id}/...`, `/media/{id}`, `/users`, `/auth/*`). `404` em
  `/api/v1/*`. Path desconhecido sem credencial responde `401` (documentado).

## 3. Instâncias, keys e cotas

- **3.1** `POST /instances` (global/admin/user-na-cota): resolve dono (sessão
  criadora; global sem sessão → admin mais antigo, sobrescrevível por
  `owner_user_id` só global/admin), gera key, persiste só o hash, devolve a key
  em claro **uma vez** (`instance_api_key`); `GET` nunca reexibe. Estender
  `instance.Service.Create` + `CreateInput{OwnerUserID}` e `instanceResponse`.
- **3.2** `POST /instances/{id}/apikey/rotate` (mint+substitui, antiga morre na
  hora, nova devolvida uma vez; serve de backfill p/ instâncias antigas) e
  `DELETE /instances/{id}/apikey` (revoga; instância volta a só-global até nova
  rotação). Só global/admin. Handlers em `internal/httpapi/apikeys.go`.
- **3.3** Na criação: `CountAll >= MaxInstances (>0)` ou `CountByOwner >= cota
  do user (>0)` → `403 quota_exceeded`; admin bypassa ambas. Toda instância
  existente conta, qualquer estado. Cota por usuário editável pelo admin
  (`PATCH /users/{id}` → `instance_quota`).
- **3.4** `CRUD /users` em `internal/httpapi/users.go` (só admin; sem registro
  público): email duplicado → `409`; remover dono com instâncias → `409` (sem
  transferência, sem cascata); dono imutável (sem endpoint de troca).

## 4. Webhooks

- **4.1** `webhook_url`/`webhook_enabled`/`webhook_events` no create/update/get
  de instância; validação em `internal/webhook/webhook.go` (`ValidateConfig`):
  URL HTTP(S), **HTTP só em loopback** (fora → `422`), tipos restritos a
  `message|receipt|connection|message.status` (desconhecido → `422`), padrão
  todos quando omitido.
- **4.2** `Envelope` ganha `event` (raw whatsmeow serializado, `omitempty` —
  aditivo ao NATS). `CutRawForLimit`: blobs acima de `MaxMediaBytes` cortados
  com omissão marcada; mídia segue via URL do envelope. `Deliver`: POST JSON com
  header `apikey:` (instance key vigente, **sem HMAC** — risco aceito +
  HTTPS), timeout 5s, 2xx = sucesso. Sem key → sem entregas (falha registrada
  sem expor a key); rotação troca a credencial imediatamente. Verificação:
  receptor `httptest` confere header + corpo.
- **4.3** `internal/webhook/worker.go`: fila (buffer 1000, envio não-bloqueante
  no dispatch), backoff exponencial (base 1s, teto 5min, 8 tentativas) e
  dead-letter em log sem travar as seguintes. Normalização recriada a partir do
  normalizer do apime; estrutura de entrega inspirada no wuzapi. `main.go`:
  worker no shutdown entre cleaner e relay (**relay continua por último**).

## 5. Documentação Swagger

- **5.1** Deps: `github.com/swaggo/swag` (CLI via `go run …@<versão pinada>`),
  `github.com/swaggo/http-swagger` + `github.com/swaggo/files` (runtime, embed,
  distroless-OK). Anotações gerais + `@Router/@Param/@Success/@Failure` nos
  handlers (inclui `formData file` no upload, headers `apikey:`/`X-Request-Id`/
  `Idempotency-Key`), `securityDefinition: apiKey` no header `apikey:`. Gerar
  com `swag init --parseInternal -g <arquivo das anotações gerais>` (obrigatório:
  código vive em `internal/`). Gerados commitados em `docs/`.
- **5.2** Servir UI pública em `/swagger/*` (mux externo). CI: passo `swag init`
  + `git diff --exit-code` (falha com docs defasadas). Verificação: `200` no
  index, `doc.json` com rotas + securityDefinition.

## 6. Manager web

- **6.1** Fork integral pontual do template `ui/dashboard` em `manager/`
  (`npm create nuxt@latest -- -t ui/dashboard` ou clone; **sem acompanhar
  upstream**): remover `server/api` e mocks, manter shell (sidebar, dark mode,
  command palette). Adicionar `@nuxtjs/i18n` (locale EN), `qr-code-styling`,
  cliente da API (mesma origem, cookie de sessão; `apikey:` só se preciso fora
  do browser), guarda de rotas + detecção de escopo (global×instância).
  `app.baseURL: '/manager/'`. Verificação: `pnpm build` + login contra o Go.
- **6.2** Telas de instâncias (lista+estado, criar com key exibida uma vez p/
  cópia, editar, remover **com confirmação digitando o nome**), visões
  admin (tudo + contas) e user (só as próprias, sem gerência). Banner "sem key"
  + botão gerar p/ instâncias antigas.
- **6.3** Pareamento: QR renderizado (canvas), polling de `qr_expires_at` e
  `status`; reemissão automática ao expirar; `connected` sem novo pareamento.
- **6.4** Telas de keys (copiar/rotate/revogar), webhook (URL, tipos assinados,
  habilitação, erro p/ HTTP não-loopback) e contas/cotas (admin).
- **6.5** Envio de teste (texto + mídia multipart) com tracking até
  `sent`/`failed`, `numbers/check`, consulta de mensagens.
- **6.6** `nuxi generate` → `.output/public` → `go:embed` no Go, servido em
  `/manager` com fallback SPA p/ `index.html`. Dockerfile multi-stage
  (Node/pnpm → Go; Node só no build), CI com job de build do manager (Node
  pinado, lockfile). Verificação: `/manager` serve, refresh em rota interna
  funciona, assets sob o subpath.

## 7. Integração e verificação final

- **7.1** README (contrato novo, `/manager`, `/swagger`, webhooks, cotas),
  AGENTS.md (revogar fronteira "sem usuários", novas rotas/envs/ordem de
  shutdown com webhook worker) e specs arquivadas, tudo com **BREAKING** onde
  há quebra (header, paths, env, `media.url`, corpo de criação). Sem notices
  novos (referências recriadas, não copiadas).
- **7.2** Gates: `go vet ./...` → `golangci-lint run` → `go test ./...`
  (com Postgres) → `go build ./...` + `pnpm build` do manager; `gofmt -l .`
  vazio; frescura do Swagger.
- **7.3** Roteiro pelo manager (cobre o 8.4 pendente do foundation): seed admin,
  conta cliente, instância, QR real + pareamento, envio real texto+mídia,
  webhook recebido (URL+tipos), recibos, disconnect + delete. Registrar
  evidências no change antes do archive.
