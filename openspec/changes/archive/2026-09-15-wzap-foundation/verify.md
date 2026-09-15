# Verify — wzap-foundation

Data: 2026-09-15. Executado via `go test`, `go vet`, `go build`, `docker compose` (stack local).

## Portas automáticas — PASS

- `gofmt -l .` → limpo
- `go vet ./...` → limpo
- `go test ./... -count=1` → todos os pacotes `ok` (integração Postgres pulada sem `WZAP_TEST_DATABASE_URL`, por desenho)
- `go build ./...` → ok
- `docker compose ps` → `postgres`, `nats`, `wzap` healthy; `GET /readyz` → `ready` (`migrations`, `nats`, `postgres` ok)
- Boot publica `whatsapp web version refreshed` (versão buscada ao vivo, ex. `2.3000.1047613002`) — hardening adicionado nesta sessão (ver Retrospective)

## 8.4 checklist manual — NÃO EXECUTADO (task permanece `[ ]`)

Tentativas de pareamento real em 2026-09-15 (instâncias `user-newest`, `FELIPE`, `testse`):
`POST .../connect` → `200`, QR gerado, polling de `/status` ok, mas o aparelho
respondeu "não foi possível conectar o dispositivo, tente novamente mais tarde"
e o canal expirou (`last_error: qr code expired`). Nenhuma instância chegou a
`connected`; sem `whatsapp_jid`, sem evento `connection`, sem envio real.

Causa raiz encontrada nesta sessão (fora do escopo da foundation, corrigido em
arquivos ainda não commitados): pin `whatsmeow b25a56d` anunciava WA web
`2.3000.1047068806` e o WhatsApp já exigia `...1047451014+` (bump upstream de
 dias depois); mais `monitorQR` que engolia `err-client-outdated`. Fix: update
para `f376da2` + erros explícitos + refresh de versão no boot. Deploy refeito e
`/readyz` ok, mas o pareamento real com o novo binário ainda NÃO foi tentado.

## Pendência

- Reexecutar o checklist do `README.md` ("Checklist manual de pareamento e envio
  real") com o binário atual e anexar as evidências aqui.
- Sugestão: mover a pendência para uma change de follow-up (ex. expor
  `POST /instances/{id}/pair-phone` como alternativa ao QR).
