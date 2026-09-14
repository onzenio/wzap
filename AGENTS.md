# wzap - Agent Instructions

Standalone Go service that manages multiple WhatsApp sessions, exposes an
authenticated REST API, and publishes durable events through NATS JetStream.

## Commands

- `go test ./... -count=1` - run the complete test suite.
- `WZAP_TEST_DATABASE_URL='postgres://wzap:secret@127.0.0.1:5432/wzap_test?sslmode=disable' go test ./... -count=1` - include Postgres integration tests.
- `WZAP_TEST_NATS_URL='nats://127.0.0.1:4222' go test ./internal/events/ -count=1` - include the NATS publisher integration test.
- `go test ./internal/<package> -run TestName -count=1` - run a focused test.
- `go vet ./...`, then `golangci-lint run`, then `go test ./...`, then `go build ./...` - CI order in `.github/workflows/ci.yml` (lint is v2.13.2, only govet/staticcheck/errcheck/ineffassign/unused in `.golangci.yml`).
- `gofmt -l .` must print nothing; `gofmt -w <files>` to fix.
- `docker compose up -d wzap` - start Postgres, NATS, and wzap locally (service at `127.0.0.1:8081`, global API key defaults to `dev-wzap-token`).
- `docker compose exec postgres createdb -U wzap wzap_test` - create the test database once (auto-created only on a fresh Postgres volume).

Go 1.26 is pinned in `go.mod`. Postgres integration tests are skipped when
`WZAP_TEST_DATABASE_URL` is absent and FAIL when the database name does not end
in `_test`; each test gets an isolated schema via
`internal/storage/postgres/postgrestest`, so never touch the shared schema in
tests. Do not report integration tests as executed unless the variable was set
and the database was reachable.

## Architecture

Wiring lives in `cmd/wzap/main.go` (`serve` default, `migrate`, `healthcheck`
subcommands). Package boundaries:

- `internal/httpapi/` owns the REST transport, the dual auth boundary
  (manager session JWT in httpOnly cookie OR machine credential in the
  `apikey:` header: global key or per-instance key), RBAC/ownership
  enforcement, and idempotency middleware. Routes live at the root (no
  prefix); `/healthz`, `/readyz`, `/swagger/*`, and `/manager/` are public.
- `internal/auth/` owns password hashing (bcrypt), instance key minting, and
  session JWTs; `internal/webhook/` owns per-instance webhook config
  validation, delivery (envelope + raw `event`, `apikey:` header), and the
  retry worker with dead-letter logging.
- `manager/` is the Nuxt console (EN, `baseURL /manager/`); `pnpm --dir
  manager build` (`nuxt generate`) renders `.output/public`, embedded into
  the binary via `manager/manager.go` (`go:embed`, SPA fallback). The
  Dockerfile builds it in a Node/pnpm stage (Node only at build time).
- `internal/instance/` owns instance lifecycle; `internal/session/session.go` is the
  engine interface, `internal/session/whatsmeow/` the adapter, `internal/session/sessiontest/`
  the fakes.
- `internal/app/` is the runtime glue: inbound events from sessions become outbox
  events + stored media.
- `internal/message/` owns outbound messages, retries, receipts, and JID
  resolution (incl. the Brazilian 9th-digit rule).
- `internal/events/` owns envelopes, subjects, the JetStream publisher, and the
  outbox relay.
- `internal/media/` owns temporary media storage on `WZAP_DATA_DIR` plus the TTL
  cleaner.
- `internal/storage/repository.go` defines persistence contracts;
  `internal/storage/postgres/` implements them; `internal/storage/migrations/`
  owns the goose-embedded schema (applied by `wzap migrate` or
  `WZAP_AUTO_MIGRATE=true` on boot).
- `internal/config/` loads all `WZAP_*` env vars (empty = unset, missing required
  var fails boot naming the variable); `internal/instancelock/` holds
  process-local locks; `internal/model/` the shared domain types.

Shutdown order is fixed: drain HTTP, then stop outbox, media cleaner, webhook
worker, and the relay last (relay publishes pending events), all within a
shared 10 s deadline; a second signal aborts immediately. At boot the service serves even if NATS is
down (`/readyz` reports it) and the relay ensures the stream when the broker
returns.

## Conventions And Gotchas

- Keep internal imports rooted at module `wzap`.
- Preserve the REST envelopes (`{"data": ...}` / `{"error": {"code", "message"}}`
  + `X-Request-Id`) and the versioned event contract (`event_version: 1`,
  stable `event_id` sent as `Nats-Msg-Id`) documented in `README.md` and
  `openspec/specs/`. Mark breaking contract changes with **BREAKING**.
- Delivery is at-least-once; event IDs must remain stable across retries and
  consumers dedupe by `event_id`.
- The supported runtime is one replica; locks are process-local.
- The service is independent of its consumers. It manages its own operator
  accounts (admin/user with instance ownership, quotas, and keys); tenant,
  account, and business-domain concepts stay outside this repository;
  consumers correlate an instance through the opaque `external_ref`.
- Never commit secrets or local environment files (`.env` overrides the compose
  dev API key).
- Use conventional commits with concise scopes such as `feat(api):`,
  `fix(events):`, and `docs(specs):`.

## Spec-Driven Workflow (OpenSpec + Superpowers)

- Workflow follows the `superpowers-bridge` conventions (brainstorm → proposal
  → specs → design → tasks → plan → verify → retrospective), encoded in this
  file plus `openspec/config.yaml` — no installed schema directory (deliberate:
  the CLI tracks built-in `spec-driven`; the rest is agent-enforced).
- OpenSpec owns WHAT and WHY in `openspec/changes/<name>/` (`brainstorm.md`,
  `proposal.md`, `specs/`, `design.md`, `tasks.md`, `plan.md`, plus `verify.md`
  and `retrospective.md` post-apply); capability specs live in
  `openspec/specs/`. Rules live in `openspec/config.yaml`.
- Superpowers owns implementation quality: brainstorming only for fuzzy
  requirements, TDD for behavior changes, systematic debugging for failures,
  code review before commits, verification before completion.
- Settled requirements go directly through `openspec-propose`; implementation
  uses `openspec-apply-change` (worktree + subagents, apply requires `plan.md`);
  finished changes get `verify.md` + `retrospective.md` BEFORE the PR and are
  archived last with `openspec-archive-change`.
- Never write brainstorm/plan output to `docs/superpowers/`; it belongs in the
  change directory.
- Treat `tasks.md` as the scope contract. Mark an item complete only after its
  specified verification succeeds.
