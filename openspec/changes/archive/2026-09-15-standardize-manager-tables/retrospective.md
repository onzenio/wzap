# Retrospective — standardize-manager-tables

## O que funcionou

- Padrão `UTable` renderer-only + engine client-side na página (sem `@tanstack/*` importável, sem `getPaginationRowModel` no UTable v4): contornou o limite sem nova dependência e manteve ordenação/filtro/paginação sobre o cursor acumulado.
- Composables por domínio + células por domínio evitaram abstração prematura; páginas viraram composição fina.
- Relatos por lote (`.superpowers/sdd/plan/batch-*-report.md`) com evidência DOM deram trilha de auditoria — a re-verificação deste apply foi rápida por causa deles.
- Regra "baseline → documentar, não tocar" manteve o escopo (typecheck `process`, `parseQuota`).

## Dívidas e follow-ups (fora desta change)

1. `fix(manager)`: `parseQuota` quebra com number do `UInput` — edição de cota via UI não emite PATCH (pré-existente, visível em qualquer história de cotas).
2. `chore(manager)`: `@types/node` ausente → `nuxt typecheck` nunca fica 100% verde (só o baseline em `nuxt.config.ts`).
3. `quotaLabel` duplicado (composable + célula, com comentário de sync) — extrair para helper único quando houver terceiro uso.
4. `localeCompare` sem locale em instâncias (contas usa `'en'`).
5. Futuro: filtro server-side, virtualização e terceira tela antes de generalizar o padrão.

## Processo

- Worktree obrigatória no plano não se aplicou (código já em `main`); para changes só-de-verificação, permitir fast-lane explícito no plano.
- `verify.md`/`retrospective.md` criados pós-apply conforme convenção; arquivar por último.
