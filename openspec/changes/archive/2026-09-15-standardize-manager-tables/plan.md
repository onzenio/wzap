# Plano de execução — standardize-manager-tables

> Leitura obrigatória antes de implementar: `proposal.md`, `design.md`, `specs/wzap-manager/spec.md`, `tasks.md` (contrato de escopo).

**Objetivo:** migrar as listas de instâncias e contas do manager (`UCard` ad-hoc) para tabelas padronizadas `UTable` + TanStack Table com ordenação, busca e paginação client-side, sem mudar REST/NATS/Postgres.

**Arquitetura:** composables de tabela por domínio isolam colunas/estado/formatação; componentes por domínio isolam células; páginas viram composição fina (busca + `UTable` + estados + modais). Sem nova dependência (`@nuxt/ui` v4 já presente).

**Stack:** Nuxt 4, `@nuxt/ui` v4 (`UTable`/TanStack Table), `@nuxtjs/i18n` (EN), `pnpm@11.22.0`.

## Restrições globais (de `openspec/config.yaml` + repo)

- Change só de frontend: não tocar em `internal/`, não mudar contrato REST/NATS/schema Postgres; sem marca **BREAKING**.
- Textos novos só em inglês, sob `instances.table.*` e `accounts.table.*` em `manager/i18n/locales/en.json`.
- Preservar: cursor "load more" de instâncias (`next_cursor`), skeleton, `UAlert` + retry, `UEmpty`, navegação ao detalhe, coluna dono só-admin, modais de contas, erro `409` ao remover dono com instâncias, `quota 0 = ilimitado`.
- Gates: `gofmt -l .` vazio (nada em Go muda, mas verificar), `pnpm --dir manager lint`, `pnpm --dir manager typecheck`, `pnpm --dir manager build` limpos.
- TDD adaptado ao frontend (sem harness de testes unitários no manager): para cada tarefa, primeiro provocar a falha observável (`typecheck` com import inexistente ou render em dev com dados mockados), depois implementar o mínimo, depois re-verificar. Sem placeholders.

## Estratégia de worktree (obrigatória no apply)

1. Criar worktree isolada para o change (ex.: `git worktree add ../wzap-std-tables -b openspec/standardize-manager-tables`) e implementar somente nela.
2. Trabalhar por lotes na ordem abaixo; um lote = um ou dois task IDs vizinhos que formam deliverable testável.
3. Commits pequenos por task ID, mensagem convencional (`feat(manager): ...`), ex.: `feat(manager): add useInstancesTable columns and sorting`.
4. Nunca commitar `node_modules/`, `.output/`, `.env`.
5. `verify.md` e `retrospective.md` só após o apply, antes do PR; arquivar por último (`openspec-archive-change`).

## Ordem de execução e verificação por lote

| Lote | Tasks | Entrega testável | Verificação do lote |
|------|-------|------------------|---------------------|
| A — Tabela de instâncias (núcleo) | 1.1, 1.2 | Composable + componentes compilam e renderizam com mock | `pnpm --dir manager typecheck` limpo + render em dev com dados mockados |
| B — Migração da página de instâncias + i18n | 1.3, 1.4 | Página em `UTable` com paridade total | check manual no navegador: busca, ordenação, paginação, load more, skeleton/erro/vazio, navegação, sem texto hardcoded (`rg` não acha string fora de `en.json`) |
| C — Tabela de contas (núcleo) | 2.1, 2.2 | Composable + componentes compilam e renderizam com mock | `pnpm --dir manager typecheck` limpo + render em dev com dados mockados |
| D — Migração da página de contas + i18n | 2.3, 2.4 | Página em `UTable` com modais/409 preservados | check manual no navegador: busca, ordenação, paginação, criar/editar cota/remover (incl. `409`), sem texto hardcoded |
| E — Verificação final | 3.1, 3.2 | Gates verdes + paridade visual | `pnpm --dir manager lint && pnpm --dir manager typecheck && pnpm --dir manager build` limpos + revisão admin/user × mobile/desktop |

Regra: não iniciar o lote seguinte com o lote atual vermelho.

---

### Task 1.1 — Criar `useInstancesTable`

**Arquivos:**
- Criar: `manager/app/composables/useInstancesTable.ts`
- Consumir (só leitura): `manager/app/composables/useInstances.ts`, `manager/app/types/api.ts` (`Instance`), `manager/i18n/locales/en.json` (chaves `instances.columns.*` existentes)
- Teste/verificação: `pnpm --dir manager typecheck`

**Interface (contrato que 1.2/1.3 vão consumir — não renomear sem atualizar o plano):**
- `useInstancesTable(items: Ref<Instance[]>, ownerEmails: Ref<Record<string,string>>, isAdmin: Ref<boolean>)` retorna `{ columns, sorting, globalFilter, pagination, tableState }` onde:
  - `columns`: colunas `name` (ordenável, accessor `name`), `status` (ordenável, accessor `status`), `external_ref` (não ordenável), `owner` (visível só se `isAdmin`, valor via `ownerLabel`-like), `whatsapp_jid` (visibilidade condicional mobile, não ordenável).
  - `sorting: Ref<SortingState>` (default `[{ id: 'name', desc: false }]`), `globalFilter: Ref<string>`, `pagination: Ref<{ pageIndex, pageSize }>` com `pageSize` default 10.
  - Busca filtra `name` + `external_ref` sobre os itens já carregados (sem chamada API).

**Micro-passos TDD:**
- [ ] Passo 1 (falha): na página atual, adicionar temporariamente `import { useInstancesTable } from '~/composables/useInstancesTable'` e rodar `pnpm --dir manager typecheck` — deve FALHAR com "Cannot find module".
- [ ] Passo 2 (implementação mínima): criar `manager/app/composables/useInstancesTable.ts` com definição de colunas TanStack (`ColumnDef<Instance>[]` via `getSortedRowModel`, `getFilteredRowModel`, `getPaginationRowModel`), estado `sorting`/`globalFilter`/`pagination`, helper `ownerLabel(instance)` interno replicando a lógica atual de `pages/instances/index.vue:24-29` (sem `t('common.notSet')` hardcoded — usar `t()`), coluna `owner` com `enableHiding`/`meta.ifAdmin`.
- [ ] Passo 3 (passa): rodar `pnpm --dir manager typecheck` — esperado PASS limpo.
- [ ] Passo 4 (commit): `git add manager/app/composables/useInstancesTable.ts && git commit -m "feat(manager): add useInstancesTable columns and sorting"`.

### Task 1.2 — Células/colunas de instâncias

**Arquivos:**
- Criar: `manager/app/components/instances/InstancesTableNameCell.vue` (nome + `external_ref` truncados), `manager/app/components/instances/InstancesTableStatusCell.vue` (embrulha `InstanceStatusBadge.vue` existente), `manager/app/components/instances/InstancesTableOwnerCell.vue` (dono admin), `manager/app/components/instances/InstancesTableJidCell.vue` (JID mono).
- Reutilizar sem modificar: `manager/app/components/instances/InstanceStatusBadge.vue`
- Verificação: render em dev com dados mockados + `pnpm --dir manager typecheck`

**Micro-passos:**
- [ ] Passo 1 (falha): referenciar `<InstancesTableStatusCell :status="'connected'" />` numa rota dev temporária — FALHA (componente inexistente).
- [ ] Passo 2: criar os 4 componentes, cada um com props tipadas (`{ instance: Instance }` ou `{ status: InstanceStatus }`), sem texto hardcoded (labels via `t('instances.columns.*')` / `t('instances.status.*')`).
- [ ] Passo 3: montar página dev temporária (não commitar) ou Storybook-like com 3 instâncias mockadas (com/sem `external_ref`, com/sem `whatsapp_jid`, dono presente/ausente) e confirmar render idêntico ao cartão atual.
- [ ] Passo 4: `pnpm --dir manager typecheck` limpo; remover scaffold temporário.
- [ ] Passo 5 (commit): `git add manager/app/components/instances/InstancesTable*.vue && git commit -m "feat(manager): extract instances table cells"`.

### Task 1.3 — Migrar `pages/instances/index.vue` para `UTable`

**Arquivos:**
- Modificar: `manager/app/pages/instances/index.vue` (linhas 138-178: bloco `UCard v-for` → `UInput` busca + `UTable` + `UPagination` + botão "load more" preservado)
- Consumir: `useInstancesTable` (1.1), células (1.2), `useInstances()` (`listInstances`, `listAccounts`) — nenhuma chamada nova à API.
- Verificação: manual no navegador

**Micro-passos:**
- [ ] Passo 1: manter intactos `loadFirst`/`loadMore`/`onCreated`/`loadOwners` (linhas 31-85); trocar só o template: `UInput v-model="globalFilter"` (placeholder `t('instances.table.search')` — chave criada em 1.4, usar fallback temporário igual ao texto final para não quebrar typecheck), `UTable :data :columns :sorting :global-filter :pagination`, `@select` → `navigateTo('/instances/...')`, `UPagination` client-side sobre itens acumulados, botão `t('common.loadMore')` exibido quando `nextCursor !== ''`, mais contagem "N loaded" (explicita limite client-side, cf. design Risco 2).
- [ ] Passo 2: preservar `pending` (skeleton), `failure` + retry (`loadFirst`), `UEmpty`, responsividade (colunas `owner`/`whatsapp_jid` ocultas em mobile via `columnVisibility`).
- [ ] Passo 3 (verificação manual obrigatória): com backend local (`docker compose up -d wzap`), checar busca (nome + `external_ref`, sem nova chamada — aba Network), ordenação nome/estado crescente/decrescente, paginação mantendo filtro, "load more" acumulando e re-ordenando, criar instância aparecendo no topo como `disconnected`, skeleton/erro-com-retry/vazio, clique navegando ao detalhe.
- [ ] Passo 4 (commit): `git add manager/app/pages/instances/index.vue && git commit -m "feat(manager): migrate instances list to UTable"`.

### Task 1.4 — i18n EN `instances.table.*`

**Arquivos:**
- Modificar: `manager/i18n/locales/en.json` (adicionar bloco `instances.table`: `search`, `loadedCount`, `pageOf`, `noResults`, `sortAsc`, `sortDesc` — só EN, sem PT-BR)
- Verificação: ausência de hardcoded

**Micro-passos:**
- [ ] Passo 1: adicionar chaves e trocar qualquer literal introduzido em 1.3 por `t('instances.table.*')`.
- [ ] Passo 2: rodar `rg -n "Buscar|Pesquisar|Carregar mais|No instances" manager/app/pages/instances manager/app/components/instances manager/app/composables/useInstancesTable.ts` — esperado zero ocorrências de texto de UI fora de `en.json` (strings de código como `name`/`status` como accessor são ok).
- [ ] Passo 3: `pnpm --dir manager typecheck` limpo.
- [ ] Passo 4 (commit): `git add manager/i18n/locales/en.json manager/app/pages/instances/index.vue manager/app/composables/useInstancesTable.ts && git commit -m "feat(manager): add instances table i18n keys"`.

**Checkpoint do lote A+B:** typecheck limpo + checklist manual de 1.3 ok antes de tocar em contas.

### Task 2.1 — Criar `useAccountsTable`

**Arquivos:**
- Criar: `manager/app/composables/useAccountsTable.ts`
- Consumir: `manager/app/composables/useAccounts.ts`, `~/types/api` (`AccountUser`)
- Verificação: `pnpm --dir manager typecheck`

**Interface (contrato para 2.2/2.3):**
- `useAccountsTable(users: Ref<AccountUser[]>)` retorna `{ columns, sorting, globalFilter, pagination }`; colunas `email` (ordenável), `role` (ordenável, badge), `instance_quota` (ordenável, via `quotaLabel`: `0` → `t('accounts.unlimited')`), `actions` (não ordenável, emite `edit-quota`/`remove`); `pageSize` default 10 igual ao de instâncias; busca sobre `email` client-side.

**Micro-passos:** mesmos de 1.1 (import temporário → falha no typecheck → implementação mínima → typecheck limpo → commit `feat(manager): add useAccountsTable columns and sorting`).

### Task 2.2 — Células/colunas de contas

**Arquivos:**
- Criar: `manager/app/components/accounts/AccountsTableRoleCell.vue` (`UBadge role`), `manager/app/components/accounts/AccountsTableQuotaCell.vue` (`quotaLabel`), `manager/app/components/accounts/AccountsTableActionsCell.vue` (botões editar cota/remover, emite eventos — sem lógica de API dentro).
- Verificação: render em dev com mocks + typecheck.

**Micro-passos:** mesmos de 1.2; mocks cobrem `role admin/user`, `quota 0` (Unlimited) e `quota N`; ações apenas emitem, a página mantém `openQuota`/`openDelete`. Commit: `feat(manager): extract accounts table cells`.

### Task 2.3 — Migrar `pages/accounts/index.vue` para `UTable`

**Arquivos:**
- Modificar: `manager/app/pages/accounts/index.vue` (linhas 249-284: `UCard v-for` → busca + `UTable` + `UPagination`; modais criar/cota/delete intocados, linhas 288-414).
- Preservar: guarda admin (`isAdmin` → redirect), `quotaLabel`, `parseQuota`, `friendlyCreateError`/`friendlyDeleteError` (`409` dono com instâncias), toasts.
- Verificação: manual no navegador.

**Micro-passos:**
- [ ] Passo 1: trocar só a listagem; ações da linha chamam `openQuota(user)`/`openDelete(user)` existentes; criar continua com prepend (`users = [created, ...users]`).
- [ ] Passo 2 (verificação manual obrigatória como admin): busca por email sem nova chamada, ordenação email/papel/cota, paginação com filtro, editar cota reflete na linha + toast, remover conta com instâncias exibe `ownsInstances` sem remover, criar conta aparece no topo, skeleton/erro-com-retry/vazio.
- [ ] Passo 3 (commit): `git add manager/app/pages/accounts/index.vue && git commit -m "feat(manager): migrate accounts list to UTable"`.

### Task 2.4 — i18n EN `accounts.table.*`

**Arquivos:**
- Modificar: `manager/i18n/locales/en.json` (bloco `accounts.table` espelhando `instances.table`: `search`, `loadedCount`/`count`, `pageOf`, `noResults`, `sortAsc`, `sortDesc`).
- Verificação: `rg` sem hardcoded + typecheck limpo. Commit: `feat(manager): add accounts table i18n keys`.

**Checkpoint dos lotes C+D:** typecheck limpo + checklist manual de 2.3 ok.

### Task 3.1 — Gates finais do manager

**Comandos (nesta ordem, na worktree, sem cache entre eles):**
1. `pnpm --dir manager lint` — esperado saída limpa.
2. `pnpm --dir manager typecheck` — esperado saída limpa.
3. `pnpm --dir manager build` — esperado `nuxt generate` com sucesso (garante SPA fallback embutido intacto).
4. `gofmt -l .` — esperado vazio (prova de que nada em Go foi tocado: `git status --porcelain` deve listar só `manager/` + `openspec/`).

Se qualquer comando falhar: corrigir no escopo do task ID que o causou (novo commit `fix(manager): ...`), não avançar para 3.2.

### Task 3.2 — Revisão visual de paridade

**Matriz obrigatória (4 combinações × 2 telas):** admin/desktop, admin/mobile (viewport ~390px: dono/JID ocultos em instâncias), user/desktop (instâncias sem coluna dono; contas redireciona para `/`), user/mobile.
**Checklist por tela:** mesmos dados de antes, mesmas ações, mesmos estados (carregamento/falha-com-retry/vazio), inglês consistente, sem regressão no detalhe da instância nem nos modais. Registrar achados em `verify.md` (pós-apply, não neste plano).

## O que este plano explicitamente NÃO inclui (Out-of-Scope)

Filtros server-side, novas rotas, exportação CSV, edição inline, seleção em massa, virtualização, outras telas, locales além de EN, mudanças Go/NATS/Postgres/Docker/CI.

## Riscos residuais → mitigação no apply

Acúmulo de cursor em memória → page-size 10 + "load more" explícito; ordenação só sobre carregados → contagem "N loaded" visível; divergência entre tabelas → comparar lado a lado em 3.2; build estático → gate `build` em 3.1.
