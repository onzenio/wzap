# Verify — wzap-chatwoot

Change: `wzap-chatwoot` (branch `feat/wzap-chatwoot`, base `main`, fork point `9f3385f`).
Data: 2026-09-14. Todas as 45 tarefas de `tasks.md` (1.1–7.3) marcadas completas.

## Gates executados na branch (com integração)

Com `WZAP_TEST_DATABASE_URL='postgres://wzap:secret@127.0.0.1:5433/wzap_test?sslmode=disable'`
(container `wzap-product-test-postgres`) e `WZAP_TEST_NATS_URL='nats://127.0.0.1:4322'`
(container `wzap-product-test-nats`):

- `go test ./... -count=1` — **verde**: 27 pacotes `ok` (incl. `internal/chatwoot/...`,
  `internal/storage/postgres` 23.4s, `internal/session/whatsmeow` 8.4s, `cmd/wzap` 8.2s),
  4 pacotes sem testes (`internal/model`, `internal/storage`, `migrations`, `internal/version`).
  Sem esse `WZAP_TEST_DATABASE_URL`, os testes Postgres são pulados; aqui eles **executaram**.
- `go vet ./...` — limpo.
- `gofmt -l .` — vazio.
- `golangci-lint run` (v2.13.2) — `0 issues`.
- `go build ./...` — limpo.
- `openspec validate --all` — 8 passed, 0 failed (pré-sync); `openspec validate --specs`
  pós-sync — 10 passed, 0 failed (5 specs novas + `wzap-inbound-events` estendido).

## Sync de specs (pós-7.3, parte deste verify)

- Criadas: `openspec/specs/wzap-chatwoot-{config,contacts,mirror,inbound,import}/spec.md`
  (Purpose verbatim do delta, requirements ADDED sob `## Requirements` único, sem headers de delta).
- Estendida: `openspec/specs/wzap-inbound-events/spec.md` (+2 requirements: edição e remoção;
  Purpose e cenários existentes intactos).
- Sem REMOVED/RENAMED; `openspec validate --specs` verde (idempotente — re-sync não muda nada).

## Comportamento coberto por testes

- Mirror WA→Chatwoot: texto/mídias (incl. stickers), contatos (único+lista), localização,
  listas, reactions, botões/interativo (incl. PIX), pedidos, produtos, ads (thumbnail quando há bytes);
  polls/calls/notices de protocolo com `warn`+skip.
- Edit/delete/read-sync reverso + avisos operacionais pt-BR (conexão, QR com imagem+pairing-code, throttle 30s).
- Webhook aberto `POST /chatwoot/webhook/{id}`: filtros (private, `message_updated`, eco `WAID:`, bot),
  texto assinado + anexos via `Enqueue`, quoted, nota privada em falha; delete reverso, template, mark-read.
- `PUT`/`GET /instances/{id}/chatwoot` + `POST /instances/{id}/chatwoot/import` → `202 {"imported":N}`
  (N conta só mensagens), dual auth sob contrato produto, token write-only/mascarado.
- Import: ordem telefone+tempo, dedup `WAID:`, lotes, `days_limit`, placeholders, idempotência em reexecução,
  gatilhos auto (pós-pareamento, once) + manual + cron 30min (janela 6h).
- Fiação `serve()`: boot opera com NATS fora do ar (`/readyz` reporta); ordem de shutdown preservada
  (HTTP → outbox → media cleaner → relay por último; scheduler de import para antes do relay).

## Riscos operacionais conhecidos (documentados, não bloqueiam)

- Token do Chatwoot em claro (write-only na API); cifragem é v2.
- Webhook aberto por desenho (SSRF-gated: http/https, ≤3 redirects, link-local bloqueado,
  hosts privados só para a URL do Chatwoot configurada).
- Import via SQL direto no Postgres do Chatwoot (módulo isolado, URI-guarded, inerte sem URI).
- Replica única (locks process-local); entrega at-least-once com `event_id` estável.

## Pós-merge

Reexecutar `go test ./... -count=1` (mesmas URLs) sobre o resultado do merge antes de qualquer cleanup.
