## MODIFIED Requirements

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
