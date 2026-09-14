## Purpose

Entrada do Chatwoot para o WhatsApp: resposta do atendente vira mensagem enfileirada no wzap (com retry e status), mais sync reverso e comandos operacionais via conversa operacional.

## ADDED Requirements

### Requirement: Webhook aberto por instância

`POST /chatwoot/webhook/{id}` SHALL ser público (sem credencial — fiel ao Evolution, risco documentado, secret é v2) e responder `200` com corpo de bot mesmo quando nada há a fazer. Evento sem `conversation`, `private`, `message_updated` sem delete e eco `WAID:` MUST ser descartado sem efeito.

#### Scenario: Eco descartado

- **WHEN** chega evento cujo `source_id` começa com `WAID:`
- **THEN** responde `200` sem enfileirar nada (anti-loop)

#### Scenario: Nota privada

- **WHEN** chega mensagem `private`
- **THEN** responde `200` sem enviar ao WhatsApp

### Requirement: Texto e anexos do atendente

Texto SHALL enfileirar com assinatura `*nome:*` quando `sign_msg` (delimitador configurável) e markdown convertido; cada anexo SHALL ser baixado de `data_url`, armazenado e enfileirado como mídia (áudio como áudio não-PTT, resto pelo mime; extensões de desenho/documento forçam documento); caption SHALL acompanhar. Falha de envio SHALL gerar nota privada de erro na conversa. Sem correlação, quoted SHALL ser ignorado sem falhar.

#### Scenario: Resposta com anexo

- **WHEN** o atendente envia texto + imagem
- **THEN** o WhatsApp recebe o texto assinado e a imagem com caption

#### Scenario: Reply do atendente

- **WHEN** a mensagem cita outra via `in_reply_to` conhecido
- **THEN** o envio cita a mensagem original no WhatsApp

### Requirement: Sync reverso e leitura

Delete com `deleted` SHALL apagar no WhatsApp quando habilitado; template SHALL enviar texto direto sem assinatura; com `MESSAGE_READ`, o envio do atendente SHALL marcar a última recebida como lida.

#### Scenario: Apagar dos dois lados

- **WHEN** o atendente apaga mensagem com a flag ligada
- **THEN** a mensagem some no WhatsApp

### Requirement: Comandos da conversa operacional

Na conversa operacional, `init[:number]` SHALL parear (por código com número, ou QR), `status` SHALL informar estado, `clearcache` SHALL limpar caches do conector, `disconnect` SHALL desconectar a sessão; cada um SHALL confirmar na conversa.

#### Scenario: Comando status

- **WHEN** o operador envia `status` na conversa operacional
- **THEN** o estado atual da instância é respondido lá
