## Purpose

Contas humanas que operam o produto: quem instala administra, cada cliente tem
sua conta, e toda instância pertence a um dono. Máquinas seguem autenticadas
por API keys, nunca por login.

## ADDED Requirements

### Requirement: Contas e roles

O serviço SHALL manter contas de usuário com email único, senha e `role` fixa
(`admin` ou `user`); somente contas `admin` SHALL criar, listar, atualizar e
remover contas. Não SHALL existir registro público de contas.

#### Scenario: Admin cria conta de cliente

- **WHEN** uma conta `admin` cria um usuário com email inédito
- **THEN** a conta é criada com a `role` informada e pode autenticar-se

#### Scenario: Conta comum tenta gerenciar contas

- **WHEN** uma conta `user` tenta criar, listar ou remover contas
- **THEN** a operação é negada com `403`

#### Scenario: Email duplicado

- **WHEN** uma conta `admin` cria um usuário com email já usado
- **THEN** a resposta é `409` e nenhuma conta é criada

### Requirement: Login e sessão

O serviço SHALL autenticar email/senha válidos emitindo uma sessão que
identifica a conta e sua `role` nas requisições seguintes; credenciais
inválidas MUST responder `401` sem indicar qual campo falhou. Enerrar a sessão
MUST invalidá-la para uso posterior.

#### Scenario: Login válido

- **WHEN** o cliente envia email e senha corretos
- **THEN** recebe uma sessão válida que autoriza requisições como aquela conta

#### Scenario: Login inválido

- **WHEN** o cliente envia email inexistente ou senha incorreta
- **THEN** a resposta é `401` sem revelar se o email existe

### Requirement: Seed do primeiro admin

Quando nenhuma conta existir, o serviço SHALL criar a conta `admin` inicial a
partir da configuração de ambiente no boot; sem essa configuração, nenhuma
conta SHALL ser criada automaticamente.

#### Scenario: Boot com seed configurado

- **WHEN** o serviço inicia sem contas e com email/senha de admin configurados
- **THEN** a conta `admin` existe e pode autenticar-se

#### Scenario: Boot sem seed

- **WHEN** o serviço inicia sem contas e sem a configuração de seed
- **THEN** nenhuma conta é criada e o login segue respondendo `401`

### Requirement: Ownership de instâncias

Toda instância SHALL pertencer a exatamente um dono, definido na criação e
imutável depois. Contas `admin` SHALL enxergar e operar todas as instâncias;
contas `user` SHALL enxergar e operar somente as próprias, incluindo criar
novas (tornando-se donas). Acesso a instância de outro dono MUST responder
`403` (conta autenticada sem direito) e instância inexistente MUST responder
`404`.

#### Scenario: Cliente opera a própria instância

- **WHEN** uma conta `user` opera uma instância da qual é dona
- **THEN** a operação é autorizada como se fosse a key daquela instância

#### Scenario: Cliente acessa instância alheia

- **WHEN** uma conta `user` acessa uma instância de outro dono
- **THEN** a resposta é `403`

#### Scenario: Listagem escopada

- **WHEN** uma conta `user` lista instâncias
- **THEN** recebe somente as instâncias das quais é dona

### Requirement: Remoção de conta com instâncias

A remoção de uma conta que possui instâncias MUST ser recusada com `409`;
a conta só SHALL ser removida após suas instâncias serem removidas. Não SHALL
existir transferência de dono nem remoção em cascata.

#### Scenario: Remover dono com instâncias

- **WHEN** uma conta `admin` remove um usuário que possui instâncias
- **THEN** a resposta é `409` e nada é removido

#### Scenario: Remover conta sem instâncias

- **WHEN** uma conta `admin` remove um usuário sem instâncias
- **THEN** a conta é removida e seu login posterior responde `401`

### Requirement: Cotas de instâncias

O serviço SHALL limitar o número de instâncias por um teto global configurável
e por uma cota por usuário configurável pelo admin; `0` significa ilimitado e
é o padrão de ambos. Toda instância existente conta para a cota,
independentemente do estado. Criar acima da cota MUST responder
`403 quota_exceeded`. Contas `admin` SHALL bypassar ambas as cotas.

#### Scenario: Criação dentro da cota

- **WHEN** um usuário com cota disponível cria uma instância
- **THEN** a instância é criada e passa a contar para a cota

#### Scenario: Cota de usuário excedida

- **WHEN** um usuário sem saldo cria uma instância
- **THEN** a resposta é `403 quota_exceeded` e nada é criado

#### Scenario: Teto global excedido

- **WHEN** a instalação atinge o teto global e um usuário cria uma instância
- **THEN** a resposta é `403 quota_exceeded` e nada é criado

#### Scenario: Admin bypassa cotas

- **WHEN** uma conta `admin` cria uma instância com cotas esgotadas
- **THEN** a instância é criada normalmente

#### Scenario: Desconectada consome cota

- **WHEN** um usuário no limite da cota possui instâncias desconectadas
- **THEN** novas criações seguem negadas até alguma instância ser removida
