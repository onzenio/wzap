# Design: standardize-manager-tables

## Context

Ver `proposal.md` (Why). Estado atual: `manager/app/pages/instances/index.vue`
e `manager/app/pages/accounts/index.vue` renderizam listas como `UCard` em
`v-for`, com paginação por cursor só em instâncias (`next_cursor` + "load
more") e sem ordenação/busca. Restrições: console Nuxt 4 + `@nuxt/ui` v4
(que já expõe `UTable` sobre TanStack Table), i18n EN em
`manager/i18n/locales/en.json`, e fronteiras do repo (change só de frontend:
sem tocar em `internal/` nem no contrato REST/NATS).

## Goals / Non-Goals

- Goals: um padrão único de tabela (colunas declarativas + estado TanStack +
  células por domínio + composable de tabela) aplicado às duas telas, com
  ordenação/filtro/paginação client-side e paridade total de dados e ações.
- Non-Goals (design): nenhuma chamada nova à API, nenhum filtro server-side,
  nenhuma mudança de layout fora das duas listas (detalhe, modais e cartões
  seguem intactos).

## Decisions

- **Padrão `UTable` + TanStack Table com estado client-side.**
  Rationale: `UTable` já vem com `@nuxt/ui` v4 (dependência presente, sem
  custo novo) e delega ordenação/filtro/paginação ao TanStack, o padrão
  documentado do ecossistema Nuxt UI. Alternativa considerada: tabela
  artesanal com `computed` de sort/filter — descartada por duplicar lógica
  nas duas telas e divergir do design system.
- **Componentização por domínio.**
  Rationale: colunas e células variam por domínio (ex.: `InstanceStatusBadge`
  existente, coluna de dono só-admin, ações de cota/remoção em contas), então
  cada domínio ganha seus componentes de tabela
  (`manager/app/components/instances/InstancesTable*.vue`,
  `manager/app/components/accounts/AccountsTable*.vue`) reutilizando os
  badges/modais atuais. Alternativa considerada: um `DataTable` genérico
  único com slots — descartada agora por forçar abstração prematura antes de
  haver um terceiro consumidor; o padrão compartilhado vive nos composables.
- **Composables de tabela por domínio (`useInstancesTable`, `useAccountsTable`).**
  Rationale: isola definição de colunas, estado de ordenação/filtro/paginação
  e formatação (ex.: `quotaLabel`, `ownerLabel`) fora das páginas, deixando as
  páginas como composição fina (busca + tabela + modais). Reutilizam
  `useInstances`/`useAccounts` para dados. Alternativa considerada: lógica
  inline nas páginas — descartada por repetir o acoplamento atual.
- **Paginação client-side sobre os itens carregados, mantendo o cursor do servidor.**
  Rationale: a API de instâncias pagina por cursor sem ordenação/busca
  server-side; o "load more" segue como fonte de dados (acumula em `items`) e
  o TanStack pagina/ordena/filtra sobre o acumulado. Alternativa considerada:
  paginação server-side nova — descartada por exigir mudança de contrato REST
  (fora do escopo, seria **BREAKING**-adjacente).
- **Responsividade via colunas progressivas.**
  Rationale: hoje dono/JID se ocultam em telas menores (`hidden md:block`);
  o mesmo comportamento vira visibilidade condicional de coluna no TanStack,
  preservando paridade mobile. Alternativa considerada: expansão de linha em
  mobile — descartada por fugir do padrão e aumentar o escopo.
- **Strings novas só em `en.json`.**
  Rationale: convenção do console é EN (`wzap-manager`); a change não cria
  locale PT-BR. Textos de busca/ordenação/paginação entram sob
  `instances.table.*` e `accounts.table.*`.

## Risks / Trade-offs

- [Risk] Acumular páginas do cursor no cliente cresce a memória em catálogos grandes → Mitigation: paginação client-side com page-size pequeno e "load more" explícito (sem auto-acumular tudo); virtualização fica como follow-up declarado.
- [Risk] Ordenação client-side só alcança itens carregados, não o catálogo inteiro → Mitigation: deixar explícito na UI (contagem "N loaded") e documentar a limitação na tabela; server-side fica como follow-up com mudança de API.
- [Risk] Divergência visual entre as duas tabelas ao evoluir separado → Mitigation: composables seguem a mesma forma (colunas, estado, page-size default) e revisão compara as duas telas lado a lado.
- [Risk] Quebra do fallback SPA/build estático embutido → Mitigation: sem nova dependência e sem rota nova; verificação inclui `pnpm --dir manager build`.

## Migration Plan

1. Implementar por domínio (instâncias, depois contas), cada um atrás da mesma
   estrutura de página (sem feature flag: change visual interna, baixo risco).
2. Rollback: reverter os commits das páginas (componentes novos são aditivos e
   não afetam Go); sem migration de dados envolvida.
3. Deploy segue o pipeline existente (build Nuxt embutido no binário); nenhuma
   etapa nova no Dockerfile ou CI.

## Open Questions

Nenhuma — as incógnitas (page-size default, colunas ordenáveis) são detalhes de
implementação resolvidos no apply sem alterar specs, abordagem ou tasks.
