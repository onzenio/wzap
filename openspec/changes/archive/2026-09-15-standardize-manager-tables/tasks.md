## 1. Tabela de instâncias

- [x] 1.1 Criar `useInstancesTable` com colunas, ordenação, filtro e paginação client-side, verificando importação sem erro no `pnpm --dir manager typecheck`.
- [x] 1.2 Extrair células/colunas de instâncias para `components/instances/InstancesTable*.vue` reutilizando `InstanceStatusBadge`, verificando render da tabela com dados mockados em dev.
- [x] 1.3 Migrar `pages/instances/index.vue` para o padrão `UTable` mantendo cursor "load more", estados e navegação ao detalhe, verificando busca, ordenação e paginação no navegador.
- [x] 1.4 Adicionar chaves EN `instances.table.*` e verificar ausência de texto hardcoded na tabela de instâncias.

## 2. Tabela de contas

- [x] 2.1 Criar `useAccountsTable` com colunas, ordenação, filtro e paginação client-side, verificando importação sem erro no `pnpm --dir manager typecheck`.
- [x] 2.2 Extrair células/colunas de contas para `components/accounts/AccountsTable*.vue` preservando ações de cota e remoção, verificando render da tabela com dados mockados em dev.
- [x] 2.3 Migrar `pages/accounts/index.vue` para o padrão `UTable` mantendo modais, escopo admin e erro `409`, verificando busca, ordenação e paginação no navegador.
- [x] 2.4 Adicionar chaves EN `accounts.table.*` e verificar ausência de texto hardcoded na tabela de contas.

## 3. Verificação final

- [x] 3.1 Executar `pnpm --dir manager lint`, `pnpm --dir manager typecheck` e `pnpm --dir manager build`, verificando saída limpa nos três comandos.
- [x] 3.2 Revisar visualmente ambas as tabelas (admin e user, mobile e desktop), verificando paridade de dados, ações e estados com o comportamento anterior.
