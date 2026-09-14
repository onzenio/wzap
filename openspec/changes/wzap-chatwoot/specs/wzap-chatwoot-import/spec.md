## Purpose

Retrofill do histórico do WhatsApp para dentro do Chatwoot via acesso direto ao Postgres dele, com dedup e gatilhos automáticos e manuais.

## ADDED Requirements

### Requirement: Guard por URI

Todo o import SHALL exigir `WZAP_CHATWOOT_IMPORT_DB_URL` válida; sem ela, acumuladores e gatilhos SHALL ficar inertes e o resto do conector SHALL funcionar. Conexão SHALL ser pool único por processo com SSL sem verificação (mesmo regime do Evolution).

#### Scenario: Import desabilitado

- **WHEN** a URI está ausente e o pareamento completa
- **THEN** nenhum SQL no Chatwoot é tentado e o espelho segue normal

### Requirement: Acúmulo e importação com dedup

Histórico (mensagens, contatos) SHALL acumular por instância a partir do feed de history-sync; o import SHALL ordenar por telefone+tempo, dedupicar por `source_id WAID:` pré-existente, inserir em lotes e vincular `source_id` dos envios do espelho. Contatos SHALL fazer upsert por `(identifier, account_id)` com etiquetas; mensagens sem conteúdo ou sem conversa SHALL ser puladas.

#### Scenario: Reimport idempotente

- **WHEN** o import roda duas vezes sobre o mesmo histórico
- **THEN** a segunda não duplica nada (dedup por `source_id`)

### Requirement: Gatilhos

Import SHALL disparar automático pós-pareamento, manual via `POST .../chatwoot/import` e via cron de 30min (`syncLostMessages`, janela 6h); início e resultado SHALL avisar na conversa operacional em pt-BR.

#### Scenario: Mensagens perdidas

- **WHEN** mensagens do WhatsApp não espelharam nas últimas 6h
- **THEN** o próximo ciclo do cron as importa e limpa o cache do conector

### Requirement: Janela e placeholders

`days_limit` SHALL limitar a janela do histórico; mídia sem bytes SHALL virar placeholder textual quando configurado, senão SHALL ser pulada.

#### Scenario: Janela configurada

- **WHEN** `days_limit` é 7 e há histórico de 30 dias
- **THEN** só os últimos 7 dias importam
