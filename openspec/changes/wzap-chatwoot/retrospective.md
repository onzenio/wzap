# Retrospective — wzap-chatwoot

## O que funcionou

- TDD por tarefa (fakes `sessiontest` + Chatwoot fake em `httptest` + NATS/Postgres reais de teste)
  manteve cada incremento verificável; a suite completa com integração roda em ~1min.
- Contrato produto reaproveitado (`set`/`find`/`import` sob raiz + `apikey:`, envelopes `data`/`error`,
  `422` de validação) evitou uma segunda matriz de auth.
- Dedup compartilhado (`source_id WAID:`) entre espelho e import eliminou duplicatas no retrofill.
- `postgrestest` (schema isolado por teste) + gate `_test` no nome do banco deram segurança nos testes Postgres.

## O que atrasou / desviou

- Pareamento por código (`init:<number>`) e feed de history-sync exigiram níveis reais da lib pinada;
  acumuladores precisaram de isolamento por instância (tarefa 2.4).
- Regra brasileira do 9º dígito + merge de duplicatas ampliaram o escopo de contatos (tarefa 3.3).
- Dois planos em `docs/superpowers/plans/` violaram a convenção (planos pertencem ao change);
  movidos para `openspec/changes/wzap-chatwoot/plans/` neste fechamento.
- Arquivos do change (`proposal.md`, `design.md`, specs do delta) estavam untracked — só `tasks.md`
  estava commitado; commitados neste fechamento antes do merge.

## Dívidas assumidas (v2 ou changes futuros)

- Cifragem do token do Chatwoot (hoje claro, write-only).
- Secret no webhook aberto (hoje público por fidelidade ao Evolution).
- `display_id` com retry limitado em conflito `UNIQUE(account_id,display_id)` (Rails aloca fora do mutex).
- Change `wzap-product` mergeado ao `main` (1f76502) mas ainda ativo no `openspec list` — arquivar à parte.
- `wzap-foundation` ainda ativo — fora do escopo deste change.

## Sinais para a próxima

- Rodar `git status --porcelain` + `openspec validate --all` no início do fechamento teria revelado
  os untracked mais cedo.
- Validar o fork point (`git merge-base --fork-point`) antes do merge evitou surpresa com o merge
  `wzap-product` (1f76502) entrado no `main` durante o desenvolvimento.
