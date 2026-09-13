# wzap-operations Specification

## Purpose

Definir autenticação, configuração, saúde, migrações e execução do serviço no ambiente local.

## Requirements

### Requirement: Autenticação de serviço

Todos os endpoints da API, exceto saúde e prontidão, SHALL exigir token de serviço válido; ausência ou invalidade MUST responder `401` sem revelar detalhes do token.

#### Scenario: Requisição autenticada

- **WHEN** o cliente envia o token de serviço válido
- **THEN** a requisição é processada

#### Scenario: Token inválido

- **WHEN** o cliente envia token ausente ou inválido
- **THEN** a resposta é `401` sem detalhes que permitam inferir o token correto

### Requirement: Saúde e prontidão

O serviço SHALL expor liveness (processo vivo) e readiness (dependências essenciais prontas). Readiness MUST responder indisponível enquanto dependências ou migrações não estiverem prontas.

#### Scenario: Dependência indisponível

- **WHEN** o banco ou o broker estiver inacessível
- **THEN** readiness responde indisponível e liveness continua respondendo vivo

### Requirement: Configuração por ambiente

O serviço SHALL ser configurável por variáveis de ambiente e MUST falhar na inicialização, com erro claro, quando configuração obrigatória estiver ausente ou inválida.

#### Scenario: Configuração ausente

- **WHEN** uma variável obrigatória não é informada
- **THEN** a inicialização falha com mensagem identificando a variável

### Requirement: Migrações

O serviço SHALL aplicar migrações de esquema quando habilitado e MUST refletir o estado das migrações na prontidão.

#### Scenario: Migrações pendentes

- **WHEN** existem migrações não aplicadas e a aplicação automática está habilitada
- **THEN** elas são aplicadas antes de o serviço aceitar tráfego

### Requirement: Empacotamento

A imagem do serviço SHALL executar como usuário não privilegiado, conter apenas o necessário para executar o serviço e suportar verificação de saúde sem ferramentas externas.

#### Scenario: Execução como não-root

- **WHEN** o contêiner inicia
- **THEN** o processo executa sem privilégios de root

### Requirement: Execução local

Subir o ambiente SHALL iniciar o serviço junto das dependências de banco e broker, persistindo dados em volume.

#### Scenario: Ambiente completo

- **WHEN** o ambiente local é iniciado
- **THEN** o serviço fica pronto e apto a receber requisições autenticadas

### Requirement: Observabilidade mínima

Registros SHALL ser estruturados e correlacionáveis por requisição e instância; respostas MUST carregar identificador de requisição.

#### Scenario: Rastreio de requisição

- **WHEN** uma requisição é processada
- **THEN** os registros e a resposta compartilham o mesmo identificador de requisição

### Requirement: Encerramento gracioso

Ao receber sinal de término, o serviço SHALL parar de aceitar novas requisições e concluir ou liberar o trabalho em andamento dentro de um tempo limitado.

#### Scenario: Sinal de término

- **WHEN** o serviço recebe pedido de encerramento
- **THEN** ele para de aceitar requisições e finaliza o processamento em andamento no prazo definido
