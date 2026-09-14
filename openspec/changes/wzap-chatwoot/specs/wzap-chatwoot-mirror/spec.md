## Purpose

Espelho em tempo real do WhatsApp para o Chatwoot: toda mensagem, mídia, recibo e estado relevante da instância aparece na conversa correta, sem perder evento nem travar a sessão.

## ADDED Requirements

### Requirement: Consumo durável sem bloquear a sessão

O espelho SHALL consumir os eventos da instância via assinatura JetStream durável in-process, com dedup por `event_id` e `source_id WAID:<id>`; NUNCA SHALL bloquear o sink de sessão (atraso do Chatwoot não retarda o WhatsApp). Entrega é at-least-once: repetição SHALL ser idempotente no Chatwoot.

#### Scenario: Rajada sem perda

- **WHEN** N mensagens chegam com o Chatwoot lento
- **THEN** todas espelham exatamente uma vez (visível) após retry, sem travar envio/recebimento

#### Scenario: Reentrega

- **WHEN** o mesmo evento é entregue duas vezes
- **THEN** o Chatwoot não exibe duplicata (dedup por `source_id`)

### Requirement: Mapeamento de conteúdo

Texto, imagem, vídeo, áudio, documento, figurinha, contato (simples e lista), localização (com link de mapa), listas, reactions, botões PIX e anúncios SHALL espelhar com o formato do Evolution (markdown convertido, grupos com prefixo `telefone — nome`, contato/localização em texto formatado, thumbnail de anúncio). Tipo não mapeável SHALL ser ignorado com `warn`, nunca derrubar o worker.

#### Scenario: Tipos cobertos

- **WHEN** chega cada tipo suportado (texto, mídia×6, contato, localização, lista, reaction, PIX, ads)
- **THEN** a conversa exibe o conteúdo equivalente formatado

#### Scenario: Tipo desconhecido

- **WHEN** chega um tipo sem mapeamento
- **THEN** o evento é pulado com `warn` e o worker segue

### Requirement: Mídia via armazenamento local

Mídia inbound SHALL ser enviada ao Chatwoot a partir dos bytes do `media.Storage` (referência do evento); `media_omitted` SHALL repassar o motivo sem falhar o espelho do texto.

#### Scenario: Mídia espelhada

- **WHEN** mensagem com mídia armazenada espelha
- **THEN** o anexo aparece na conversa com o conteúdo

#### Scenario: Mídia omitida

- **WHEN** o evento marca `media_omitted`
- **THEN** só o texto espelha, sem erro

### Requirement: Edição, remoção e leitura

Edição SHALL criar mensagem "editada" vinculada ao original; remoção SHALL apagar no Chatwoot quando habilitado; leitura SHALL atualizar `last_seen` da conversa. Sem correlação local, a operação SHALL ser pulada com `warn`.

#### Scenario: Mensagem editada

- **WHEN** o remetente edita o texto
- **THEN** nova mensagem "editada" aparece vinculada ao original

#### Scenario: Mensagem apagada

- **WHEN** a mensagem é apagada com a flag ligada
- **THEN** a cópia no Chatwoot é removida

### Requirement: Filtros e avisos da conversa operacional

`ignore_jids`, `status@broadcast` e pesquisas SHALL ser ignorados. Conexão, QR (com imagem e pairing-code) e status SHALL chegar como mensagens da conversa operacional em pt-BR, com throttle de 30s em reconexões.

#### Scenario: JID ignorado

- **WHEN** mensagem chega de JID na lista de ignorados
- **THEN** nada espelha

#### Scenario: QR gerado

- **WHEN** novo QR de pareamento surge
- **THEN** a imagem + instruções chegam na conversa operacional
