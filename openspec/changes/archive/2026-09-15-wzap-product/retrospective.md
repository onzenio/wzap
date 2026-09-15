# Retrospective — wzap-product

## O que funcionou

- Contas/keys/webhooks/manager em cima da base da foundation sem quebrar o
  contrato versionado (`event_version: 1`, envelopes REST, `X-Request-Id`).
- Dual auth (sessão JWT em cookie OU `apikey:` global/por instância) + RBAC de
  dono unificou REST sem vazar conceito de tenant para dentro do serviço.

## O que doeu

- **7.3 nunca rodou de verdade**: sem pareamento real, a fatia manager→QR→envio→
  webhook segue não validada de ponta a ponta. A foundation 8.4 travou no mesmo
  lugar, então o risco é compartilhado, não novo.
- Parear depende de aparelho humano + IP com reputação + versão do web client —
  três variáveis fora do nosso controle que nenhum fake cobre.

## Mudanças adotadas (sessão de 2026-09-15, ainda não commitadas)

- Mesmo fix da foundation: whatsmeow `f376da2`, `monitorQR` explícito,
  `RefreshWAVersion` no boot. Aumenta muito a chance de 7.3 passar na próxima
  tentativa, mas não a substitui.

## Follow-ups

- Reexecutar 7.3 (cobre 8.4 da foundation) com o binário atual.
- Avaliar `POST /instances/{id}/pair-phone` (código de 8 dígitos) como caminho
  alternativo ao QR quando o aparelho recusar o scan.
- Commits separados por escopo ao invés de acumular debug não commitado.
