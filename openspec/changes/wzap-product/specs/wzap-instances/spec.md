## Purpose

Delta de instâncias do produto: criação emite key e dono, respeita cotas e
segue as regras de escopo de contas e keys.

## MODIFIED Requirements

### Requirement: Criação de instância

O serviço SHALL permitir criar uma instância com nome e referência externa
opcional, retornando identificador, estado inicial `disconnected`, o dono e a
instance key em claro uma única vez. O dono é a conta da sessão criadora; na
criação pela key global sem sessão, o dono é a conta `admin` mais antiga,
podendo ser sobrescrito pelo parâmetro opcional de dono (só global/admin). A
referência externa, quando informada, MUST ser única. Criação acima da cota
MUST responder `403 quota_exceeded`; somente key global, sessão `admin` ou
sessão `user` (dentro da cota) SHALL criar instâncias.

#### Scenario: Criação bem-sucedida

- **WHEN** o cliente autorizado envia nome e referência externa inexistente
- **THEN** o serviço responde `201` com `id`, `status: disconnected`, dono e a
  key em claro exibida uma única vez

#### Scenario: Referência externa duplicada

- **WHEN** o cliente autorizado tenta criar uma instância com referência
  externa já usada
- **THEN** o serviço responde `409` e nenhuma instância é criada

#### Scenario: Criação acima da cota

- **WHEN** um usuário sem saldo de cota tenta criar uma instância
- **THEN** o serviço responde `403 quota_exceeded` e nada é criado

#### Scenario: Criação pela key global sem sessão

- **WHEN** a key global cria uma instância sem informar dono
- **THEN** o dono é a conta `admin` mais antiga

#### Scenario: Criação com key de instância

- **WHEN** o cliente usa uma instance key para criar instância
- **THEN** a resposta é `403`, pois a key só opera a própria instância
