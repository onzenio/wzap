# wzap - Agent Instructions

Standalone Go service that manages multiple WhatsApp sessions, exposes an
authenticated REST API, and publishes durable events through NATS JetStream.

## Commands

- `go test ./... -count=1` - run the complete test suite.
- `WZAP_TEST_DATABASE_URL='postgres://wzap:secret@127.0.0.1:5432/wzap_test?sslmode=disable' go test ./... -count=1` - include Postgres integration tests.
- `go test ./internal/<package> -run TestName -count=1` - run a focused test.
- `go vet ./...` - run the Go static analyzer.
- `golangci-lint run` - run the configured lint gate (v2.13.2 in CI).
- `go build ./...` - compile all packages.
- `gofmt -w <files>` - format changed Go files; `gofmt -l .` must print nothing.
- `docker compose up -d` - start Postgres, NATS, and wzap locally.

Go 1.26 is pinned in `go.mod`. Integration tests are skipped when
`WZAP_TEST_DATABASE_URL` is absent, so do not report them as executed unless the
variable was set and the database was reachable.

## Architecture

- `cmd/wzap/` contains the executable and subcommands (`serve`, `migrate`, and
  `healthcheck`).
- `internal/httpapi/` owns the REST transport and authentication boundary.
- `internal/instance/` and `internal/session/` own instance lifecycle and the
  WhatsApp engine adapter.
- `internal/message/` owns outbound messages, retries, receipts, and recipient
  resolution.
- `internal/events/` owns event envelopes, subjects, JetStream, and relay.
- `internal/media/` owns temporary media storage and cleanup.
- `internal/storage/` defines persistence contracts; `storage/postgres/`
  implements them and `storage/migrations/` owns the schema.

The service is independent of its consumers. Keep user, tenant, account, and
business-domain concepts outside this repository; consumers correlate an
instance through the opaque `external_ref`.

## Conventions And Gotchas

- Keep internal imports rooted at module `wzap`.
- Preserve the REST envelopes and versioned event contract documented in
  `README.md` and `openspec/specs/`.
- Delivery is at-least-once; event IDs must remain stable across retries.
- The supported runtime is one replica; locks are process-local.
- Never commit secrets or local environment files.
- Use conventional commits with concise scopes such as `feat(api):`,
  `fix(events):`, and `docs(specs):`.

## Spec-Driven Workflow (OpenSpec + Superpowers)

- OpenSpec owns WHAT and WHY: `proposal.md`, `specs/`, `design.md`, and
  `tasks.md`, including sync and archive operations.
- Superpowers owns implementation quality: use brainstorming only for fuzzy
  requirements, TDD for behavior changes, systematic debugging for failures,
  code review before commits, and verification before completion.
- Settled requirements go directly through `openspec-propose`; implementation
  uses `openspec-apply-change`; finished changes are archived last with
  `openspec-archive-change`.
- Keep `brainstorm.md` and optional `plan.md` inside
  `openspec/changes/<name>/`; do not create a parallel planning tree.
- Treat `tasks.md` as the scope contract. Mark an item complete only after its
  specified verification succeeds.
- Lift recurring mistakes and project constraints into
  `openspec/config.yaml`.
