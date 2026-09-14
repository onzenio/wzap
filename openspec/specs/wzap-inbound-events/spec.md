# wzap-inbound-events Specification

## Purpose

Definir o contrato observável dos eventos publicados no broker para que o backend consuma mensagens recebidas, recibos e mudanças de conexão com durabilidade.

## Requirements

### Requirement: Publicação em stream durável

O serviço SHALL publicar eventos em um stream durável, com subjects por instância, cobrindo mensagem recebida, recibo, status de envio, conexão, edição e remoção.

#### Scenario: Consumidor recebe evento

- **WHEN** uma mensagem é recebida por uma instância
- **THEN** um evento é publicado no subject daquela instância e fica disponível para consumo

### Requirement: Envelope versionado

Todo evento MUST conter `event_id`, `event_version`, `type`, `instance_id`, `occurred_at` e `payload`; o conteúdo MUST ser JSON.

#### Scenario: Envelope de mensagem recebida

- **WHEN** um evento de mensagem é publicado
- **THEN** o envelope contém os campos obrigatórios e o payload da mensagem

### Requirement: Evento de mensagem recebida

O payload de mensagem recebida MUST conter remetente, conversa, se é grupo, identificador da mensagem, momento, tipo e o texto quando aplicável; quando houver mídia, MUST conter referência e metadados da mídia.

#### Scenario: Texto recebido

- **WHEN** chega uma mensagem de texto
- **THEN** o payload identifica remetente, conversa, texto e momento

#### Scenario: Mídia recebida

- **WHEN** chega uma mensagem com mídia disponível
- **THEN** o payload referencia a mídia com metadados e URL de acesso

### Requirement: Evento de recibo

O payload de recibo MUST identificar as mensagens afetadas, o novo estado (entregue, lido ou reproduzido), a conversa e o momento.

#### Scenario: Recibo de leitura

- **WHEN** um recibo de leitura é recebido
- **THEN** o evento informa as mensagens, o estado `read` e o momento

### Requirement: Evento de conexão

Mudanças de estado da instância SHALL gerar evento com o novo estado e, em falha, o motivo.

#### Scenario: Queda de conexão

- **WHEN** a instância perde a conexão
- **THEN** um evento informa o estado `disconnected` e o motivo quando disponível

### Requirement: Evento de status de envio

Mudanças de estado de mensagens enviadas SHALL gerar evento correlacionável ao identificador devolvido no aceite.

#### Scenario: Envio concluído

- **WHEN** uma mensagem enviada é confirmada
- **THEN** um evento informa `sent` e o `message_id` de origem

### Requirement: Durabilidade

A publicação SHALL sobreviver a reinícios do serviço e a indisponibilidades breves do broker, podendo haver duplicatas; `event_id` MUST ser estável para deduplicação pelo consumidor.

#### Scenario: Broker indisponível

- **WHEN** o broker fica indisponível durante a geração de um evento
- **THEN** o evento é publicado quando a conexão retorna, sem perda

### Requirement: Retenção e replay

O stream MUST reter eventos por período configurável e MUST permitir que um consumidor novo ou atrasado receba os eventos ainda retidos.

#### Scenario: Consumidor atrasado

- **WHEN** um consumidor começa a ler após uma parada dentro do período de retenção
- **THEN** ele recebe os eventos pendentes

### Requirement: Confirmação do consumidor

Eventos MUST permanecer disponíveis para reentrega até que o consumidor confirme o processamento, dentro do período de retenção.

#### Scenario: Consumidor falha antes de confirmar

- **WHEN** o consumidor não confirma o processamento
- **THEN** o evento é reentregue

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
