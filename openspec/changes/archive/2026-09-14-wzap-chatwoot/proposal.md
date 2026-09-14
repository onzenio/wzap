## Why

Operações reais de atendimento precisam do WhatsApp dentro do Chatwoot (multi-atendente, filas, histórico). O Evolution resolve isso com um conector bidirecional completo, e o wzap hoje não tem nenhum: eventos morrem no NATS sem espelho, e não há caminho de resposta do atendente para o WhatsApp. Esta change recria esse conector no wzap, em Go e dentro da arquitetura do serviço.

## What Changes

- Conector Chatwoot in-process em `internal/chatwoot` (pacotes `config`, `client`, `contacts`, `conversations`, `mirror`, `webhook`, `import`, `worker`).
- Configuração por instância via REST (`set`/`find`: url, account, token em claro, inbox, assinatura, reabertura/pendência, merge BR, import flags, `ignoreJids`) + envs globais (`WZAP_CHATWOOT_ENABLED`, `WZAP_CHATWOOT_IMPORT_DB_URL`, `WZAP_CHATWOOT_BOT_CONTACT`, `WZAP_CHATWOOT_MESSAGE_READ/DELETE`).
- Espelho WA→Chatwoot em tempo real via consumer JetStream durável in-process: contatos, conversas (cache+lock), mensagens, mídia, grupos, replies, reactions, PIX, ads, edits, deletes, read-sync, QR/status via conversa operacional.
- Webhook inbound aberto `POST /chatwoot/webhook/{id}` (Chatwoot→WA) com filtros, assinatura, anexos, quoted, delete reverso, template, mark-read e comandos da conversa operacional.
- Extensão da sessão/whatsmeow: plumbing de `ProtocolMessage` (edit/delete), `DeleteMessage`, `MarkRead`, pareamento por código e feed de history-sync.
- Import histórico via SQL direto no Postgres do Chatwoot (módulo isolado com `pgx`, guard por URI): auto pós-pareamento, manual e cron 30min.
- Tabela de correlação `chatwoot_messages` (sem purga; apaga no `DELETE` da instância, como o Evolution).
- Novas deps Go pinadas (`phonenumbers`, `imaging`) e novos subjects de evento (edit/delete).
- **Depende de `wzap-product`**: rotas e auth seguem o contrato produto (raiz + `apikey:`).

## Out-of-Scope

- Secret/HMAC no webhook (v2); i18n das mensagens da conversa operacional (pt-BR fixo); cifragem do token em repouso (v2).

## Capabilities

### New Capabilities

- `wzap-chatwoot-config`: configuração por instância e envs globais.
- `wzap-chatwoot-mirror`: espelho WA→Chatwoot em tempo real.
- `wzap-chatwoot-inbound`: entrada Chatwoot→WA, conversa operacional e sync reverso.
- `wzap-chatwoot-contacts`: contatos, conversas, inbox, regra BR e labels.
- `wzap-chatwoot-import`: import histórico via SQL e gatilhos.

### Modified Capabilities

- `wzap-inbound-events`: novos tipos de evento (edição e remoção de mensagem) no mesmo envelope versionado.

## Impact

- **Novo**: pacote `internal/chatwoot`, 2 tabelas + migration goose, 4 endpoints REST, 1 consumer JetStream, 1 cron, cliente HTTP Chatwoot próprio.
- **Tocado**: `session.EventSink` (+2 métodos), camada whatsmeow (protocol, delete, mark-read, pairing-code, history-sync), subjects NATS (+2), fakes de sessão.
- **Sistemas**: exige Chatwoot operante por instância configurada; import exige acesso ao Postgres do Chatwoot; sem URI o espelho funciona sem labels/import.
- Referência (Evolution) usada como estudo para **recriar** — sem copiar código.
