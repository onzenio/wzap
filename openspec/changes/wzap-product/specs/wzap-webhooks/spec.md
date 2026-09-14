## Purpose

Entrega de eventos por HTTP para sistemas externos por instância, no envelope
versionado do barramento acrescido do evento cru, com credencial simples e
reentrega limitada.

## ADDED Requirements

### Requirement: Configuração por instância

Cada instância SHALL ter configuração própria de webhook (`url`, `enabled`,
`events`), gerenciável por quem pode operar a instância. `events` SHALL ser
uma lista dos tipos (`message`, `receipt`, `connection`, `message.status`) com
padrão todos; tipo desconhecido MUST ser rejeitado. Somente URLs HTTP(S)
SHALL ser aceitas, e HTTP não-loopback MUST ser rejeitado. Webhook
desabilitado ou sem URL MUST NOT gerar nenhuma entrega.

#### Scenario: Habilitar webhook

- **WHEN** o operador configura `url` válida e `enabled: true`
- **THEN** os eventos seguintes da instância passam a ser entregues nela

#### Scenario: URL inválida

- **WHEN** o operador configura URL malformada, com esquema não HTTP(S) ou
  HTTP fora de loopback
- **THEN** a configuração é rejeitada com `422` e a anterior é mantida

#### Scenario: Webhook desabilitado

- **WHEN** eventos ocorrem com o webhook desabilitado ou sem URL
- **THEN** nenhuma entrega é tentada e nada é enfileirado

#### Scenario: Assinatura de tipos

- **WHEN** o operador configura `events` com um subconjunto válido
- **THEN** somente esses tipos são entregues; os demais seguem só no NATS

#### Scenario: Tipo desconhecido

- **WHEN** o operador configura `events` com tipo inexistente
- **THEN** a configuração é rejeitada com `422` e a anterior é mantida

### Requirement: Entrega dos eventos

O serviço SHALL entregar os tipos assinados no envelope versionado do
barramento, acrescido do campo `event` com o evento whatsmeow serializado,
via POST com corpo JSON. Blobs acima do limite de mídia SHALL ser cortados do
`event` cru com a omissão marcada; a mídia segue referenciada pela URL do
envelope. A entrega é at-least-once e MUST carregar identificador estável
para deduplicação pelo recebedor.

#### Scenario: Evento entregue

- **WHEN** um evento de tipo assinado ocorre numa instância com webhook
  habilitado
- **THEN** o serviço POSTa envelope + `event` cru na URL configurada

#### Scenario: Tipo não assinado

- **WHEN** ocorre um evento de tipo fora da assinatura
- **THEN** nenhuma entrega HTTP é tentada para ele

#### Scenario: Reentrega idempotente

- **WHEN** a mesma entrega é repetida após falha
- **THEN** o recebedor observa o mesmo identificador de evento da tentativa
  anterior

#### Scenario: Cru acima do limite

- **WHEN** o evento cru contém blobs acima do limite de mídia
- **THEN** os blobs são cortados com omissão marcada e o restante é entregue

### Requirement: Credencial apikey na entrega

Cada entrega SHALL carregar a instance key vigente no header `apikey:`, para
o recebedor comparar. Não SHALL haver assinatura HMAC: sem integridade
criptográfica, a confidencialidade depende de HTTPS (obrigatório fora de
loopback). Instância sem key MUST NOT ter entregas; sem key válida no momento
da entrega, a tentativa MUST ser registrada como falha sem expor a key.
Rotacionar a key MUST trocar a credencial das entregas seguintes
imediatamente.

#### Scenario: Credencial confere

- **WHEN** o recebedor compara o header `apikey:` com a instance key
- **THEN** os valores conferem

#### Scenario: Rotação troca a credencial

- **WHEN** a instance key é rotacionada
- **THEN** entregas seguintes carregam a nova key no header

### Requirement: Retry exponencial limitado

Falha de entrega (erro de rede, timeout ou status fora de 2xx) SHALL gerar
retentativas com espera exponencial até um limite de tentativas/tempo; ao
esgotar, a entrega MUST ser descartada e registrada como dead-letter em log,
sem bloquear entregas seguintes. Entrega 2xx SHALL encerrar as retentativas.

#### Scenario: Sucesso após falha

- **WHEN** a URL falha duas vezes e responde 2xx na terceira tentativa
- **THEN** a entrega é concluída e nenhuma tentativa a mais ocorre

#### Scenario: Falha persistente

- **WHEN** a URL nunca responde 2xx dentro do limite
- **THEN** a entrega é descartada, registrada como dead-letter e as seguintes
  prosseguem normalmente
