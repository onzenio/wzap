## Purpose

Delta: dois novos tipos de evento no mesmo barramento e envelope versionado, para sustentar o espelho de edição/remoção sem mudar nenhuma semântica existente.

## ADDED Requirements

### Requirement: Evento de edição de mensagem

Edição de mensagem recebida SHALL publicar evento de edição no subject da instância, com o mesmo envelope (`event_id` novo e estável, `event_version`, `type` de edição) e payload com `message_id` original, novo conteúdo e momento. Consumidor SHALL deduplicar por `event_id`.

#### Scenario: Edição publicada

- **WHEN** o remetente edita uma mensagem recebida
- **THEN** um evento de edição aparece no subject da instância com o vínculo ao original

### Requirement: Evento de remoção de mensagem

Remoção/revoke de mensagem recebida SHALL publicar evento de remoção no subject da instância, no mesmo envelope, com `message_id` e momento. Sem o original conhecido, o consumidor SHALL pular com `warn`.

#### Scenario: Remoção publicada

- **WHEN** uma mensagem recebida é apagada
- **THEN** um evento de remoção aparece no subject da instância
