## Purpose

Contatos, conversas e inbox resolvidos por instância com concorrência segura, regra brasileira e degradação graciosa sem o Postgres do Chatwoot.

## ADDED Requirements

### Requirement: Conversa por remetente com lock

A resolução SHALL usar cache (≈30min) + lock por remetente (≈30s, espera até 5s com double-check): reutiliza conversa aberta da inbox (ou `pending` com `conversation_pending`; reabre com `reopen_conversation`), senão cria. Concorrentes SHALL convergir para a mesma conversa, sem duplicar.

#### Scenario: Rajada do mesmo remetente

- **WHEN** 10 mensagens do mesmo remetente chegam juntas
- **THEN** uma única conversa recebe as 10

#### Scenario: Conversa resolvida

- **WHEN** há conversa resolvida e `reopen_conversation` é falso
- **THEN** nova conversa é criada em vez de reusar

### Requirement: Contatos com regra brasileira

Busca SHALL tentar variantes com/sem 9º dígito para `+55`; duplicatas BR SHALL fundir quando `merge_brazil_contacts`; grupos usam identificador próprio e nome com `(GROUP)`; nome/avatar SHALL atualizar quando vazio ou divergente. Falha de criação SHALL retornar nulo com `warn`, e o espelho da mensagem SHALL ser pulado sem derrubar o worker.

#### Scenario: Contato com/sem nove

- **WHEN** o número existe com nono dígito diferente do cadastrado
- **THEN** o contato correto é encontrado (e fundido se configurado)

### Requirement: Inbox e labels

A inbox SHALL ser localizada por nome (cache) e criada como canal `api` com o `webhook_url` quando ausente no fluxo `auto_create`. Etiquetar contatos SHALL exigir a URI do Postgres do Chatwoot; sem ela, o fluxo SHALL seguir sem label com `warn`.

#### Scenario: Sem Postgres do Chatwoot

- **WHEN** a URI está ausente e um contato novo espelha
- **THEN** contato e conversa existem; só falta a etiqueta, com `warn` em log

### Requirement: Correlação de IDs

Cada envio espelhado SHALL registrar (`instance_id`, `wa_key`, `chatwoot_message_id`, `conversation_id`, `inbox_id`, origem, lida) para replies, deletes, edits e dedup; linhas SHALL viver até o `DELETE` da instância (sem purga, como o Evolution).

#### Scenario: Reply dias depois

- **WHEN** o WhatsApp cita mensagem espelhada há 20 dias
- **THEN** o vínculo `in_reply_to` resolve corretamente
