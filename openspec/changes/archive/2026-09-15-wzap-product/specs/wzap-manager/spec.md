## Purpose

Console web do produto: quem instala administra tudo, cada cliente opera as
próprias instâncias (incluindo parear o próprio celular), sem tocar em
terminal ou REST manual.

## ADDED Requirements

### Requirement: Servido pelo próprio serviço

O console SHALL ser servido pelo serviço em `/manager`, em inglês, sem exigir
rede externa em runtime para seus próprios assets. Rotas internas do console
MUST resolver para o aplicativo (navegação direta e refresh funcionam).

#### Scenario: Acesso direto a rota interna

- **WHEN** o navegador abre diretamente uma rota interna do console
- **THEN** o aplicativo carrega e exibe a tela correspondente

### Requirement: Login e visões

O acesso SHALL exigir login por email/senha; sem sessão válida, qualquer tela
MUST redirecionar ao login. Contas `admin` SHALL ver a visão de operador
(todas as instâncias, contas, cotas); contas `user` SHALL ver somente as
próprias instâncias, com ações fora do escopo ausentes ou desabilitadas.

#### Scenario: Login de operador

- **WHEN** uma conta `admin` entra com credenciais válidas
- **THEN** vê todas as instâncias e a gerência de contas

#### Scenario: Login de cliente

- **WHEN** uma conta `user` entra com credenciais válidas
- **THEN** vê somente as próprias instâncias, sem gerência de contas

#### Scenario: Sessão ausente

- **WHEN** um visitante sem sessão abre qualquer tela
- **THEN** é redirecionado ao login

### Requirement: Gestão de instâncias

O console SHALL permitir listar (com estado), criar, editar, desconectar e
remover instâncias conforme o escopo da conta. A remoção SHALL exigir
confirmação digitada com o nome da instância.

#### Scenario: Criar instância

- **WHEN** a conta cria uma instância dentro da cota
- **THEN** a instância aparece na lista como `disconnected` com sua key exibida
  uma vez para cópia

#### Scenario: Remoção com confirmação

- **WHEN** a conta remove uma instância e digita o nome corretamente
- **THEN** a instância é removida; com nome divergente, nada acontece

#### Scenario: Cota excedida

- **WHEN** a conta tenta criar acima da cota
- **THEN** o console exibe o erro de cota sem criar nada

### Requirement: Pareamento com QR

O console SHALL exibir o QR de pareamento renderizado visualmente, com validade
e atualização automática (reemissão ao expirar e transição ao conectar), por
instância no escopo da conta.

#### Scenario: Parear pelo console

- **WHEN** a conta abre o pareamento de uma instância desconectada
- **THEN** vê o QR válido e, após a leitura no celular, o estado exibido vira
  `connected`

#### Scenario: QR expira na tela

- **WHEN** o QR exibido expira antes da leitura
- **THEN** um novo QR é exibido automaticamente sem ação manual

### Requirement: Keys e webhook

O console SHALL permitir copiar a key exibida uma vez na criação, rotacionar e
revogar a key (escopo admin/global na API, refletido na UI), e configurar
`url`/`enabled` do webhook por instância. Keys em claro MUST NOT ser reexibidas
após a primeira exibição.

#### Scenario: Rotacionar key

- **WHEN** a conta rotaciona a key de uma instância
- **THEN** a nova key é exibida uma vez para cópia e a antiga deixa de valer

#### Scenario: Configurar webhook

- **WHEN** a conta salva `url` válida, escolhe os tipos de evento e habilita o
  webhook
- **THEN** a configuração persiste e passa a valer para os próximos eventos

#### Scenario: Webhook com URL insegura

- **WHEN** a conta tenta salvar URL HTTP fora de loopback
- **THEN** o console exibe o erro de validação sem salvar

### Requirement: Envio de teste e mensagens

O console SHALL permitir verificar número, enviar mensagem de teste (texto e
mídia) e acompanhar o estado até `sent`/`failed`, além de consultar mensagens
da instância, tudo no escopo da conta.

#### Scenario: Envio de teste com acompanhamento

- **WHEN** a conta envia um texto de teste para um número válido
- **THEN** acompanha o estado até o desfecho `sent` ou `failed` com motivo

#### Scenario: Número inexistente

- **WHEN** a conta verifica ou envia para número inexistente no WhatsApp
- **THEN** o console exibe a falha sem enfileirar envio
