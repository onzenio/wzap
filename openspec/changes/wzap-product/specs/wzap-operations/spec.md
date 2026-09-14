## Purpose

Delta de operação do produto: nova fronteira de autenticação, rotas sem
prefixo, superfícies servidas e documentação interativa.

## MODIFIED Requirements

### Requirement: Autenticação de serviço

Todos os endpoints da API, exceto saúde, prontidão, console e documentação,
SHALL exigir credencial válida: sessão de usuário OU header `apikey:`.
Credencial ausente ou inválida MUST responder `401` sem revelar detalhes;
credencial válida sem escopo para a operação MUST responder `403`.

#### Scenario: Requisição autenticada

- **WHEN** o cliente envia sessão válida ou apikey válida com escopo
- **THEN** a requisição é processada

#### Scenario: Token inválido

- **WHEN** o cliente envia credencial ausente ou inválida (incluindo o esquema
  legado `Authorization: Bearer`)
- **THEN** a resposta é `401` sem detalhes que permitam inferir credencial
  válida

#### Scenario: Sem escopo

- **WHEN** o cliente envia credencial válida para operação fora do seu escopo
- **THEN** a resposta é `403`

## ADDED Requirements

### Requirement: Rotas na raiz sem prefixo

As rotas da API SHALL viver na raiz, sem prefixo de versão (`/instances`,
`/instances/{id}/messages/text`, `/media/{id}`, …). O prefixo `/api/v1`
anterior MUST NOT ser mais atendido. URLs de mídia embutidas em eventos SHALL
usar o path sem prefixo.

#### Scenario: Rota na raiz

- **WHEN** o cliente autenticado chama a rota sem prefixo
- **THEN** a operação é processada normalmente

#### Scenario: Prefixo legado

- **WHEN** o cliente chama qualquer rota sob `/api/v1`
- **THEN** a resposta é `404`, como rota inexistente

#### Scenario: URL de mídia sem prefixo

- **WHEN** um evento referencia mídia armazenada
- **THEN** a URL usa o path sem prefixo e o download naquela URL funciona

### Requirement: Superfícies servidas

O serviço SHALL servir o console em `/manager` e a documentação interativa em
`/swagger/*`, ambas sem exigir credencial para carregar; as chamadas de dados
seguem as regras de autenticação de cada plano.

#### Scenario: Console carrega sem credencial

- **WHEN** o navegador abre `/manager` sem sessão
- **THEN** o aplicativo carrega e apresenta o login

#### Scenario: Documentação carrega sem credencial

- **WHEN** o cliente abre `/swagger/` sem credencial
- **THEN** a documentação interativa carrega

### Requirement: Documentação interativa da API

A documentação servida SHALL descrever as rotas, corpos, envelopes de resposta
e o esquema `apikey`, permitindo testar chamadas com uma key informada pelo
leitor. A documentação MUST refletir as rotas efetivamente servidas.

#### Scenario: Testar chamada pela documentação

- **WHEN** o leitor informa uma apikey válida na documentação e executa uma
  chamada
- **THEN** a chamada é enviada com o header `apikey:` e a resposta é exibida
