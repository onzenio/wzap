# wzap-instances Specification

## Purpose

Gerenciar o ciclo de vida das instâncias de WhatsApp (pareamento, estado de conexão, reconexão e remoção) para que o backend opere múltiplas contas isoladas sem lidar com o protocolo.

## Requirements

### Requirement: Criação de instância

O serviço SHALL permitir criar uma instância com nome e referência externa opcional, retornando um identificador e o estado inicial `disconnected`. A referência externa, quando informada, MUST ser única.

#### Scenario: Criação bem-sucedida

- **WHEN** o cliente autenticado envia nome e referência externa inexistente
- **THEN** o serviço responde `201` com `id`, `status: disconnected` e a referência registrada

#### Scenario: Referência externa duplicada

- **WHEN** o cliente autenticado tenta criar uma instância com referência externa já usada
- **THEN** o serviço responde `409` e nenhuma instância é criada

### Requirement: Pareamento por QR

O serviço SHALL iniciar o pareamento de uma instância não conectada, retornando um QR code e sua validade, e MUST transicionar o estado para `pairing`. Após a leitura do QR, o estado MUST virar `connected` com o identificador público da conta registrado.

#### Scenario: Início de pareamento

- **WHEN** o cliente solicita conectar uma instância `disconnected`
- **THEN** o serviço responde com o QR e a expiração, e o estado passa a `pairing`

#### Scenario: QR expirado

- **WHEN** o QR expira sem leitura e o cliente consulta o estado de pareamento
- **THEN** o serviço fornece um novo QR válido

#### Scenario: Pareamento concluído

- **WHEN** o usuário lê o QR no aplicativo
- **THEN** o estado passa a `connected`, o identificador público é registrado e um evento de conexão é publicado

### Requirement: Instância já conectada

O serviço SHALL ser idempotente ao receber pedido de conexão para instância já logada, sem gerar novo pareamento.

#### Scenario: Conectar instância conectada

- **WHEN** o cliente solicita conectar uma instância `connected`
- **THEN** o serviço responde `connected` sem emitir QR

### Requirement: Reconexão automática

Em queda transitória, o serviço SHALL tentar reconectar com espera crescente e MUST tornar o estado observável durante as tentativas.

#### Scenario: Queda transitória

- **WHEN** a conexão cai por falha de rede temporária
- **THEN** o estado reflete a tentativa de reconexão e volta a `connected` quando restabelecida

### Requirement: Desconexão definitiva

O serviço SHALL encerrar a sessão ao receber pedido de desconexão, remover as credenciais da instância e MUST NOT reconectar automaticamente depois disso.

#### Scenario: Desconectar instância

- **WHEN** o cliente solicita desconectar uma instância conectada
- **THEN** o estado vira `disconnected`, as credenciais são removidas e nenhuma reconexão é tentada

### Requirement: Restrição da conta

Quando a conta for restringida pela plataforma WhatsApp, o serviço SHALL marcar a instância com estado `error`, registrar o motivo e MUST NOT tentar reconexão automática.

#### Scenario: Restrição detectada

- **WHEN** a plataforma informa restrição ou banimento da conta
- **THEN** o estado vira `error` com motivo registrado e não há reconexão automática

### Requirement: Remoção de instância

O serviço SHALL permitir remover uma instância, encerrando a sessão e eliminando dados associados; operações posteriores sobre a instância MUST responder `404`.

#### Scenario: Remoção concluída

- **WHEN** o cliente remove uma instância existente
- **THEN** sessão, mensagens e mídias associadas são eliminadas e consultas seguintes respondem `404`

### Requirement: Restauração no boot

O serviço SHALL restaurar automaticamente as sessões registradas ao iniciar, com concorrência limitada, e MUST refletir o resultado de cada restauração no estado da instância.

#### Scenario: Reinício do serviço

- **WHEN** o serviço reinicia com sessões persistidas
- **THEN** as instâncias voltam a `connected` sem novo pareamento
