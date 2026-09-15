## Why

As telas de instâncias (`manager/app/pages/instances/index.vue`) e contas (`manager/app/pages/accounts/index.vue`) listam registros como cartões `UCard` ad-hoc, sem ordenação, busca ou paginação padronizadas, o que dificulta localizar registros e manter as telas conforme o catálogo cresce.

## What Changes

- Ambas as listas passam a usar tabelas padronizadas sobre `UTable` + TanStack Table (ordenação, filtro/busca e paginação client-side, em inglês conforme convenção do console).
- Colunas das instâncias: nome, estado, referência externa, dono (só admin) e JID WhatsApp, com navegação à linha de detalhe preservada.
- Colunas das contas: email, papel (`role`) e cota de instâncias, com ações existentes de editar cota e remover preservadas.
- Componentização por domínio: colunas/células extraídas para componentes (`manager/app/components/instances/`, `manager/app/components/accounts/`) e lógica de tabela isolada em composables dedicados.
- Estados de carregamento (skeleton), falha (com retry), vazio e paginação por cursor ("load more") mantêm o comportamento atual.

## Out-of-Scope

- Mudanças no contrato REST, nos eventos NATS ou no schema Postgres; sem marca **BREAKING** (change só de frontend).
- Novas rotas, filtros server-side, exportação CSV, edição inline, seleção em massa ou virtualização.
- Padronização de outras telas (detalhe da instância, mensagens, overview, login) — ficam para changes futuras.
- Internacionalização além do inglês existente (sem novos locales; sem PT-BR na UI, mantendo a convenção `wzap-manager`).

## Capabilities

### New Capabilities

(nenhuma — change de padronização de UI sobre comportamento existente)

### Modified Capabilities

- `wzap-manager`: gestão de instâncias e contas passa a exigir apresentação tabular padronizada (ordenação, busca e paginação client-side) com os mesmos dados, escopos e ações atuais.

## Impact

- Afeta só o console (`manager/app/pages/instances/index.vue`, `manager/app/pages/accounts/index.vue`, novos componentes/composables, chaves em `manager/i18n/locales/en.json`); nenhum impacto em Go, NATS, Postgres ou Docker.
- Dependência já presente: `@nuxt/ui` v4 (com `UTable`/TanStack) — sem nova dependência.
- Verificação via `pnpm --dir manager lint`, `pnpm --dir manager typecheck` e `pnpm --dir manager build`, mais revisão visual das duas telas.
