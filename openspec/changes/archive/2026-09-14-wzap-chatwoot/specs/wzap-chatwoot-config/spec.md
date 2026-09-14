## Purpose

Configuração do conector Chatwoot por instância, mais chaves globais do processo, com validação explícita e segredos sob responsabilidade operacional.

## ADDED Requirements

### Requirement: Configuração por instância

Cada instância SHALL ter configuração própria (`enabled`, `url`, `account_id`, `token`, `name_inbox`, `sign_msg`, `sign_delimiter`, `reopen_conversation`, `conversation_pending`, `merge_brazil_contacts`, `import_contacts`, `import_messages`, `days_limit`, `auto_create`, `organization`, `logo`, `ignore_jids`), gerenciável por quem pode operar a instância. Com `enabled: true`, `url`, `account_id`, `token`, `sign_msg`, `reopen_conversation` e `conversation_pending` SHALL ser obrigatórios; URL inválida, segredo ausente ou `sign_msg` não-booleano MUST ser rejeitado com `422` sem alterar a configuração anterior. `name_inbox` vazio SHALL assumir o identificador da instância; `sign_delimiter` vazio SHALL assumir quebra de linha. O `token` SHALL ser guardado em claro (risco operacional documentado, cifragem é v2).

#### Scenario: Habilitar conector

- **WHEN** o operador grava config válida com `enabled: true`
- **THEN** o espelho e o webhook da instância passam a operar

#### Scenario: Config inválida

- **WHEN** o operador grava URL malformada ou omite `account_id`/`token`
- **THEN** a gravação é rejeitada com `422` e a anterior é mantida

#### Scenario: Leitura sem configuração

- **WHEN** a instância nunca foi configurada
- **THEN** a leitura responde desligado com campos vazios

### Requirement: Chaves globais

O processo SHALL expor `WZAP_CHATWOOT_ENABLED` (gate global; desligado bloqueia `set`/`find`/espelho/webhook com `400`), `WZAP_CHATWOOT_IMPORT_DB_URL` (URI do Postgres do Chatwoot; ausente desliga import e labels sem desligar o espelho), `WZAP_CHATWOOT_BOT_CONTACT`, `WZAP_CHATWOOT_MESSAGE_READ` e `WZAP_CHATWOOT_MESSAGE_DELETE`.

#### Scenario: Gate global desligado

- **WHEN** `WZAP_CHATWOOT_ENABLED` não é `true` e chega qualquer operação do conector
- **THEN** a operação é rejeitada com `400` e nada é executado

#### Scenario: Sem URI de import

- **WHEN** a URI do Postgres do Chatwoot está ausente e o espelho opera
- **THEN** mensagens espelham normalmente; só import e labels ficam indisponíveis com `warn` em log

### Requirement: Auto-criação de inbox

Com `auto_create: true`, a gravação SHALL garantir inbox `api` com o `webhook_url` da instância (reusando por nome), o contato operacional e a mensagem `init[:number]`; a resposta SHALL incluir o `webhook_url` a cadastrar no Chatwoot.

#### Scenario: Auto-criação

- **WHEN** o operador grava com `auto_create: true` e a inbox não existe
- **THEN** inbox, contato operacional e mensagem de init passam a existir e a resposta traz o `webhook_url`
