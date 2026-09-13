# wzap-media Specification

## Purpose

Armazenar temporariamente e servir mídias recebidas e enviadas com integridade, autenticação e expiração.

## Requirements

### Requirement: Download automático de mídia recebida

Mensagens com mídia SHALL ter o conteúdo baixado automaticamente até o limite configurado; acima do limite, o evento MUST indicar omissão da mídia sem interromper o fluxo.

#### Scenario: Mídia dentro do limite

- **WHEN** chega mensagem com mídia de tamanho dentro do limite
- **THEN** o conteúdo é armazenado e referenciado no evento

#### Scenario: Mídia acima do limite

- **WHEN** chega mídia maior que o limite configurado
- **THEN** o evento é publicado com indicação de mídia omitida

### Requirement: Armazenamento com integridade

A mídia armazenada MUST registrar tipo, tamanho, nome quando houver e checksum, permitindo verificar integridade.

#### Scenario: Mídia armazenada

- **WHEN** uma mídia é armazenada
- **THEN** seus metadados e checksum ficam registrados e associados à mensagem

### Requirement: Download autenticado

O acesso à mídia SHALL exigir autenticação de serviço; mídia inexistente ou expirada MUST responder `404`; a resposta MUST informar o tipo de conteúdo correto.

#### Scenario: Download válido

- **WHEN** o cliente autenticado baixa uma mídia existente
- **THEN** recebe o conteúdo com o tipo correto

#### Scenario: Acesso sem credencial

- **WHEN** o acesso ocorre sem token válido
- **THEN** a resposta é `401`

### Requirement: Expiração e limpeza

Mídias MUST expirar após o período configurado, e arquivos e registros expirados SHALL ser removidos periodicamente.

#### Scenario: Mídia expirada

- **WHEN** o período de retenção termina
- **THEN** a mídia deixa de estar disponível e seus arquivos são removidos

### Requirement: Upload para envio

Mídias enviadas por upload MUST ser validadas quanto a tipo permitido e tamanho antes do enfileiramento; inválidas MUST responder `422`.

#### Scenario: Upload válido

- **WHEN** o cliente envia arquivo permitido dentro do limite
- **THEN** a mensagem é enfileirada referenciando a mídia armazenada

### Requirement: Identificadores opacos

Identificadores de mídia MUST ser opacos e não sequenciais, dificultando acesso não autorizado.

#### Scenario: Identificador não adivinhável

- **WHEN** uma mídia é criada
- **THEN** seu identificador não revela sequência nem conteúdo previsível
