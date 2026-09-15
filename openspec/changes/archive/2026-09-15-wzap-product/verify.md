# Verify — wzap-product

Data: 2026-09-15. Portas automáticas executadas no stack local.

## Portas automáticas — PASS

- `gofmt -l .` → limpo
- `go vet ./...` → limpo
- `go test ./... -count=1` → todos os pacotes `ok` (integração Postgres pulada sem `WZAP_TEST_DATABASE_URL`, por desenho)
- `go build ./...` → ok
- `GET /readyz` → `ready` (`migrations`, `nats`, `postgres` ok)

## 7.3 verificação E2E pelo manager — NÃO EXECUTADA (task permanece `[ ]`)

Seed admin, conta cliente, instância, QR real, envio real e webhook recebido não
foram exercitados de ponta a ponta: o pareamento real travou no mesmo ponto da
foundation 8.4 (aparelho: "não foi possível conectar o dispositivo, tente
novamente mais tarde"; servidor: `qr code expired`, sem `connected`).

Causa raiz e fix saíram nesta sessão (fora do escopo do product, arquivos ainda
não commitados): `whatsmeow b25a56d` anunciava WA web `2.3000.1047068806` contra
`...1047451014+` exigido; `monitorQR` engolia `err-client-outdated`. Fix: update
para `f376da2` + erros explícitos + `RefreshWAVersion` no boot (log confirma
`whatsapp web version refreshed`, ex. `2.3000.1047613002`). Deploy refeito, mas
a ponta real (QR + envio + webhook) ainda NÃO foi reexecutada.

## Pendência

- Reexecutar 7.3 com o binário atual (cobre também o 8.4 da foundation,
  arquivada com a mesma pendência) e anexar as evidências aqui, ou mover para
  change de follow-up.
