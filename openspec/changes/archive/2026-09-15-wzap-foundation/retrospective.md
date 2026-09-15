# Retrospective — wzap-foundation

## O que funcionou

- Contrato first (specs por capability) + TDD sustentou 45/46 tasks sem retrabalho
  de desenho; fakes de sessão isolaram o protocolo nos testes.
- Outbox transacional + relay + `event_id` estável deram durabilidade real sem
  complicar os serviços.
- Uma réplica por desenho simplificou locks e restore no boot.

## O que doeu

- **8.4 travou no mundo real**: pareamento QR falhou com toast genérico no
  aparelho e `qr code expired` no servidor — zero diagnóstico do motivo.
- **Duas causas em nosso código/dependência** (achadas investigando o upstream
  `tulir/whatsmeow` pin→HEAD):
  1. Pin desatualizado anunciava WA web velho; WhatsApp rejeita pareamento de
     versão antiga no momento da confirmação (o QR chega a ser emitido).
  2. `monitorQR` engolia os eventos terminais `err-client-outdated`,
     `err-scanned-without-multidevice` e `err-unexpected-state` num Debug log.
- Processo da comunidade que não tínhamos: atualizar whatsmeow com frequência
  e/ou buscar a versão em runtime (`GetLatestVersion` + `SetWAVersion`).

## Mudanças adotadas (sessão de 2026-09-15, ainda não commitadas)

- `go.mau.fi/whatsmeow` → `f376da2`; `monitorQR` com erros explícitos
  (`failPairing` + testes); `RefreshWAVersion` no boot (não-fatal, 10s timeout).

## Follow-ups

- Reexecutar 8.4 com o binário atual; considerar `POST /instances/{id}/pair-phone`
  (código de 8 dígitos) como alternativa ao QR.
- Rotina: checar bumps do whatsmeow antes de cada release que toque em sessão.
- Commits separados por escopo (`feat(session):`, `chore(deps):`) ao invés de um
  commit único da sessão de debug.
