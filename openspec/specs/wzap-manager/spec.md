# wzap-manager Specification

## Purpose

Console web do produto: quem instala administra tudo, cada cliente opera as
próprias instâncias (incluindo parear o próprio celular), sem tocar em
terminal ou REST manual.

## Requirements

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

O console SHALL apresentar as instâncias em tabela padronizada com ordenação,
busca e paginação client-side, mantendo criar, editar, desconectar e remover
instâncias conforme o escopo da conta. A remoção SHALL exigir confirmação
digitada com o nome da instância.

#### Scenario: Localizar instância na tabela

- **WHEN** a conta digita parte do nome ou da referência externa na busca da tabela
- **THEN** a tabela exibe somente as instâncias carregadas que correspondem, sem nova chamada à API

#### Scenario: Ordenar instâncias

- **WHEN** a conta aciona a ordenação em uma coluna ordenável (nome ou estado)
- **THEN** as linhas carregadas são reordenadas de forma crescente/decrescente

#### Scenario: Paginar instâncias

- **WHEN** a conta navega entre páginas da tabela
- **THEN** vê uma fatia dos registros carregados com a posição atual indicada, sem perder o filtro aplicado

#### Scenario: Criar instância

- **WHEN** a conta cria uma instância dentro da cota
- **THEN** a instância aparece na tabela como `disconnected` com sua key exibida
  uma vez para cópia

#### Scenario: Remoção com confirmação

- **WHEN** a conta remove uma instância e digita o nome corretamente
- **THEN** a instância é removida; com nome divergente, nada acontece

#### Scenario: Cota excedida

- **WHEN** a conta tenta criar acima da cota
- **THEN** o console exibe o erro de cota sem criar nada

### Requirement: Gestão de contas em tabela

O console SHALL apresentar as contas em tabela padronizada com ordenação,
busca e paginação client-side, mantendo criar conta, editar cota e remover
conta (só admin). Quotas `0` SHALL significar ilimitado; remover dono com
instâncias SHALL responder `409` sem remover nada.

#### Scenario: Localizar conta na tabela

- **WHEN** o admin digita parte do email na busca da tabela
- **THEN** a tabela exibe somente as contas que correspondem, sem nova chamada à API

#### Scenario: Ordenar contas

- **WHEN** o admin aciona a ordenação em uma coluna ordenável (email, papel ou cota)
- **THEN** as linhas são reordenadas de forma crescente/decrescente

#### Scenario: Editar cota a partir da tabela

- **WHEN** o admin edita a cota de uma conta pela ação da linha
- **THEN** a linha reflete a nova cota e um aviso de sucesso é exibido

#### Scenario: Remover conta com instâncias

- **WHEN** o admin tenta remover uma conta que ainda possui instâncias
- **THEN** o console exibe o erro de conflito sem remover a conta

### Requirement: Estados e escopos das tabelas

As tabelas SHALL preservar os escopos por papel (admin vê tudo, `user` vê só as
próprias instâncias e não vê a gerência de contas), a coluna de dono visível
só para admin, e os estados de carregamento, falha com retry e vazio, em inglês.

#### Scenario: Visão do cliente

- **WHEN** uma conta `user` abre as instâncias
- **THEN** vê somente as próprias instâncias, sem a coluna de dono

#### Scenario: Falha de carregamento

- **WHEN** o carregamento da lista falha
- **THEN** a tabela exibe o erro com ação de retry que recarrega os dados

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
