# wzap

Gateway independente para WhatsApp. O wzap mantém sessões multi-instância,
expõe um contrato REST de comandos para aplicações consumidoras e publica os
eventos recebidos (mensagens, recibos, conexão e status de envio) em um stream
durável do NATS JetStream.

## Visão e escopo

- Serviço Go independente (`module wzap`), operado por contas humanas
  (`admin`, `user`) e consumido por integrações via API keys. O serviço
  conhece apenas contas de operação e donos de instância: a instância é
  identificada por `id`, por um `external_ref` opcional e único e por um
  `owner_user_id` imutável cujo significado de negócio pertence ao
  consumidor.
- Múltiplas instâncias de WhatsApp em um processo, com sessões persistidas no
  Postgres (`whatsmeow`/sqlstore) e restauradas no boot.
- Envio assíncrono (`202` + `message_id`) de texto, localização, contato e
  mídia (imagem, vídeo, áudio/PTT e documento), com idempotência por
  `Idempotency-Key` e estados observáveis.
- Eventos publicados por instância com envelope versionado e entrega
  at-least-once via outbox transacional (sem perda em queda do broker ou do
  processo; duplicatas possíveis, deduplicáveis por `event_id`).
- Mídia recebida baixada automaticamente até o limite configurado, armazenada
  com checksum e servida por download autenticado enquanto não expira.

Fora de escopo na v1: grupos/newsletters/stories, webhooks globais, SQLite,
Redis, métricas Prometheus, escala horizontal e implementações de
consumidores. O serviço roda como **uma réplica**; locks são em memória e o
boot adquire um advisory lock do Postgres (segunda réplica aborta).

> **BREAKING (produto):** o contrato abaixo substitui o contrato headless
> anterior — header `apikey:` no lugar de `Authorization: Bearer`, rotas na
> raiz sem `/api/v1`, `WZAP_API_KEY` no lugar de `WZAP_SERVICE_TOKEN` e
> `media.url` sem prefixo. Consumidores da API antiga precisam migrar
> (detalhes em cada seção).

## Requisitos

- Go 1.26+ (o `go.mod` pina `go 1.26.0`) para build/testes locais.
- Postgres 18 (banco `wzap`; outro banco `_test` para os testes de
  integração) e NATS 2 com JetStream habilitado (`-js`).
- Docker + Docker Compose para a operação local completa (imagem distroless,
  binário estático, usuário não-root, healthcheck embutido).
- `golangci-lint` v2.13.2 (versão usada no CI) para o lint local.

## Configuração (`WZAP_*`)

Todas as variáveis são lidas do ambiente no boot. Vazio é tratado como não
informado. Variável obrigatória ausente ou valor malformado falha a
inicialização com mensagem nomeando a variável.

| Variável | Obrigatória | Padrão | Descrição |
| --- | --- | --- | --- |
| `WZAP_API_KEY` | sim | — | **BREAKING:** substitui `WZAP_SERVICE_TOKEN` (ignorada se presente). Key global de máquina, enviada no header `apikey:`, com acesso total. |
| `WZAP_JWT_SECRET` | sim | — | Segredo HMAC das sessões do manager (cookies JWT). |
| `WZAP_ADMIN_EMAIL` | não | vazio | Seed do primeiro admin no boot quando não há contas; sem ela (ou sem `WZAP_ADMIN_PASSWORD`) nada é criado. |
| `WZAP_ADMIN_PASSWORD` | não | vazio | Senha inicial do admin seed (troque após instalar). |
| `WZAP_MAX_INSTANCES` | não | `0` | Teto global de instâncias; `0` = ilimitado. Acima responde `403 quota_exceeded`. |
| `WZAP_DEFAULT_USER_INSTANCE_QUOTA` | não | `0` | Cota padrão de instâncias por usuário; `0` = ilimitado; admin pode ajustar por conta. |
| `WZAP_DATABASE_URL` | sim | — | URL do Postgres, ex. `postgres://wzap:secret@postgres:5432/wzap?sslmode=disable`. |
| `WZAP_NATS_URL` | sim | — | URL do NATS, ex. `nats://nats:4222`. |
| `WZAP_HTTP_ADDR` | não | `:8080` | Endereço de escuta do HTTP. O subcomando `healthcheck` resolve host vazio/`0.0.0.0`/`::` para `127.0.0.1`. |
| `WZAP_PUBLIC_URL` | não | vazio | Base das URLs de download de mídia embutidas nos eventos (`media.url`). Sem ela a URL sai relativa (`/media/<id>`); na operação local use `http://127.0.0.1:8081`. |
| `WZAP_NATS_STREAM` | não | `WZAP` | Nome do stream JetStream. O stream cobre `wzap.>`. |
| `WZAP_EVENT_RETENTION_DAYS` | não | `7` | Retenção dos eventos no stream, em dias. |
| `WZAP_DATA_DIR` | não | `/data` | Raiz dos arquivos de mídia (o volume `wzap-media` no compose). |
| `WZAP_MEDIA_TTL_SECONDS` | não | `7200` | TTL da mídia armazenada, em segundos (2 h). |
| `WZAP_MAX_MEDIA_BYTES` | não | `16777216` | Tamanho máximo de mídia recebida/enviada, em bytes (16 MiB). |
| `WZAP_OUTBOX_WORKERS` | não | `4` | Goroutines de envio do outbox. |
| `WZAP_HUMANIZE` | não | `false` | Simula presença/atraso antes do envio (humanização). |
| `WZAP_LOG_LEVEL` | não | `info` | Nível do `log/slog` (`debug`, `info`, `warn`, `error`). |
| `WZAP_LOG_FORMAT` | não | `json` | Formato dos logs (`json` ou `text`). |
| `WZAP_AUTO_MIGRATE` | não | `true` | Aplica as migrações embutidas no boot antes de aceitar tráfego. |

## Contrato REST

Base: `/` (raiz, sem prefixo). **BREAKING:** o prefixo `/api/v1` foi
eliminado; qualquer rota sob `/api/v1/*` responde `404`.

Toda rota da API exige credencial válida em um dos dois planos (mesmas rotas,
mesmos efeitos no escopo):

- Máquinas: header `apikey:` com a key global (acesso total) ou a instance
  key (controle total da própria instância, menos gerenciar a própria key).
  **BREAKING:** `Authorization: Bearer` não é mais aceito.
- Humanos (manager): sessão JWT em cookie httpOnly (`POST /auth/login` com
  email/senha, `POST /auth/logout`, `GET /auth/me`).

Ausente ou inválida responde `401`; válida sem escopo responde `403`.
Sucesso responde no envelope `{"data": ...}` e erro em
`{"error": {"code", "message"}}`; toda resposta carrega `X-Request-Id` (eco
do enviado ou gerado) e os logs da requisição usam o mesmo identificador.

### Saúde e prontidão (sem autenticação)

| Método e rota | Resposta |
| --- | --- |
| `GET /healthz` | `200` com `{"data":{"status":"ok"}}` enquanto o processo vive. |
| `GET /readyz` | `200` quando Postgres, migrações e NATS estão prontos; `503` com `{"data":{"status":"unready","checks":{...}}}` caso contrário. |

Públicas sem credencial: `GET /healthz`, `GET /readyz`, o console em
`/manager` e a documentação em `/swagger/*` (as chamadas de dados seguem as
regras de autenticação acima).

Subcomandos do binário: `wzap serve` (padrão), `wzap migrate` (aplica as
migrações e sai) e `wzap healthcheck` (chama `/readyz` em loopback e sai
`0`/`1`; é o healthcheck do container).

### Contas (só key global ou sessão `admin`; sem registro público)

| Método e rota | Corpo/Resposta |
| --- | --- |
| `POST /users` | `{"email","password","role"}` → `201`; email duplicado → `409`. |
| `GET /users` | `200` com a lista. |
| `GET /users/{id}` | `200` com a conta; `404` se não existir. |
| `PATCH /users/{id}` | `{"instance_quota"}` → `200` (só a cota é editável). |
| `DELETE /users/{id}` | `204`; dono com instâncias → `409` (sem transferência, sem cascata). |

No primeiro boot sem contas e com `WZAP_ADMIN_EMAIL`/`WZAP_ADMIN_PASSWORD`,
o serviço cria o admin inicial (e adota as instâncias legadas sem dono);
sem as vars, nenhuma conta é criada.

### Instâncias

| Método e rota | Corpo/Resposta |
| --- | --- |
| `POST /instances` | `{"name","external_ref"?,"owner_user_id"?,"webhook_*"?}` → `201` com a instância, o dono e `instance_api_key` em claro **uma única vez**; `external_ref` duplicada → `409`; acima da cota → `403 quota_exceeded`. O dono é a sessão criadora, ou o admin mais antigo (sobrescrevível por `owner_user_id` só global/admin) na criação por key global. |
| `GET /instances?limit&cursor` | `200` com `{"items":[...],"next_cursor"}`; `limit` padrão `50`, máximo `100`; cursor inválido → `400`. Contas `user` recebem só as próprias; instance key → `403`. |
| `GET /instances/{id}` | `200` com a instância (dono + webhook, nunca a key); `404` se não existir (id malformado também é `404`); instância de outro dono → `403`. |
| `PATCH /instances/{id}` | `{"name"?,"external_ref"?,"webhook_url"?,"webhook_enabled"?,"webhook_events"?}` → `200`; `external_ref` vazia limpa a referência; webhook inválido → `422`. |
| `DELETE /instances/{id}` | `204`; encerra a sessão e apaga mensagens e mídias; operações seguintes → `404`. |
| `POST /instances/{id}/apikey/rotate` | Só global/admin → `200` com a nova key em claro uma vez; a antiga morre na hora. Serve de backfill para instâncias antigas sem key. |
| `DELETE /instances/{id}/apikey` | Só global/admin → `204`; a instância volta a responder só pela global até nova rotação. |
| `POST /instances/{id}/connect` | Inicia o pareamento → `200` com `{status, qr_code, qr_expires_at}`; instância já `connected` → `200` com `{status:"connected"}` sem QR (idempotente); já em `pairing` devolve o QR atual. |
| `GET /instances/{id}/qr` | `200` com o QR atual e a validade; reemite um QR novo quando o anterior expirou ou o pareamento ainda não começou; `409` se a sessão já está conectada. |
| `POST /instances/{id}/disconnect` | `204`; encerra a sessão, remove as credenciais e não reconecta. |
| `GET /instances/{id}/status` | `200` com `{status, whatsapp_jid, last_error, last_connected_at}`. |
| `POST /instances/{id}/numbers/check` | `{"phone"}` → `200` com `{exists, jid, normalized}`; número malformado/ausente do WhatsApp vem `exists:false`; sessão sem resolução confiável → `503`. |

Estados de instância: `disconnected`, `pairing`, `connected`, `error`. Restrição
de conta vira `error` com motivo e **não** reconecta automaticamente; queda
transitória reconecta com espera crescente.

Cotas: teto global (`WZAP_MAX_INSTANCES`) + cota por usuário (ajustável em
`PATCH /users/{id}`); `0` = ilimitado. Toda instância conta, em qualquer
estado; acima da cota a criação responde `403 quota_exceeded`. Contas
`admin` (e a key global) bypassam ambas.

### Webhooks

Cada instância tem webhook próprio (`webhook_url`, `webhook_enabled`,
`webhook_events`, gerenciáveis no create/update/get por quem opera a
instância). `events` aceita `message`, `receipt`, `connection` e
`message.status` (padrão: todos); tipo desconhecido → `422`. Só URLs HTTP(S);
HTTP fora de loopback → `422` (fora de loopback use HTTPS). Sem URL, com o
webhook desabilitado ou sem instance key, nada é entregue.

A entrega é um POST JSON com o envelope versionado acrescido do `event` cru
(whatsmeow serializado; blobs acima do limite são cortados com a omissão
marcada, a mídia segue pela URL do envelope) e a instance key vigente no
header `apikey:` — sem HMAC, a confidencialidade depende de HTTPS. Falhas
(rede, timeout, status fora de 2xx) geram retries exponenciais limitados;
esgotados, a entrega é descartada como dead-letter em log sem travar as
seguintes. A entrega é at-least-once: deduplique pelo `event_id` estável.

### Manager e Swagger

- Console web em `/manager` (inglês, sem credencial para carregar; sem
  sessão redireciona ao login): admin vê tudo + contas/cotas; `user` vê só
  as próprias instâncias (lista, criação com key exibida uma vez, edição,
  remoção com confirmação digitada, QR com polling, keys, webhook, envio de
  teste e mensagens). Build Nuxt estático embutido no binário.
- Documentação interativa em `/swagger/*` (Swagger 2.0, esquema
  `apiKey` no header `apikey:`); o CI falha com docs defasadas.

### Mensagens

| Método e rota | Corpo/Resposta |
| --- | --- |
| `POST /instances/{id}/messages/text` | `{"to","text"}` → `202` com `{"message_id","status":"queued"}`. |
| `POST /instances/{id}/messages/location` | `{"to","latitude","longitude"}` → `202`. |
| `POST /instances/{id}/messages/contact` | `{"to","display_name","vcard"}` → `202`. |
| `POST /instances/{id}/messages/media` | `multipart/form-data`: `to`, `type` (`image\|video\|audio\|document`), `file` e opcionais `caption`, `filename`, `ptt` (`true`/`1`) → `202`. Tipo declarado precisa bater com o `Content-Type` do arquivo. |
| `GET /instances/{id}/messages?limit&cursor` | `200` com `{"items":[...],"next_cursor"}`; `limit` padrão `50`, máximo `100`. |
| `GET /instances/{id}/messages/{message_id}` | `200` com estado atual, marcos temporais (`delivered_at`, `read_at`), tentativas (`attempts`) e `last_error`. |
| `GET /media/{id}` | Conteúdo bruto (fora do envelope) com `Content-Type`/`Content-Length` corretos; sem credencial → `401`; mídia inexistente ou expirada → `404`. |

O destinatário aceita telefone em formato livre: o serviço resolve o JID
canônico do WhatsApp aplicando a regra brasileira do 9º dígito antes de
enfileirar. Número inexistente → `422`; instância não conectada → `409`; mídia
inválida, tipo não suportado ou acima de `WZAP_MAX_MEDIA_BYTES` → `422`.

Os quatro POSTs de envio aceitam `Idempotency-Key` opcional (TTL de 24 h,
escopo por instância): repetir a mesma key com o mesmo conteúdo devolve a
resposta original com `X-Idempotent-Replay: true`; mesma key com conteúdo
diferente → `422`; enquanto a original corre → `409`. Sem a key o
comportamento é o envio normal.

Ciclo de vida da mensagem: `queued → sending → sent|failed`, com retries
exponenciais (até 5 falhas transitórias, teto de 2 min por espera) e retomada
de mensagens presas em `sending` por mais de 5 min. Recibos de entrega/leitura
atualizam `delivered_at`/`read_at` e geram o evento de recibo.

Tipos de mídia aceitos no upload: `image/jpeg`, `image/png`, `image/webp`,
`video/mp4`, `video/3gpp`, `audio/aac`, `audio/amr`, `audio/mpeg`, `audio/mp4`,
`audio/ogg`, `application/pdf`, `text/plain`, `.doc`, `.xls`, `.ppt`, `.docx`,
`.xlsx` e `.pptx`.

## Chatwoot

Conector opcional que espelha mensagens do WhatsApp no Chatwoot em tempo real
e importa o histórico via SQL direto no Postgres do Chatwoot. Sem
`WZAP_CHATWOOT_IMPORT_DB_URL` o import fica inerte e o espelho segue normal.

| Variável | Padrão | Descrição |
| --- | --- | --- |
| `WZAP_CHATWOOT_ENABLED` | `false` | Liga o conector (espelho, webhook, import). |
| `WZAP_CHATWOOT_BOT_CONTACT` | `123456` | Identificador do contato operacional (avisos de conexão e import). |
| `WZAP_CHATWOOT_MESSAGE_READ` | `false` | Projeta recibos de leitura no Chatwoot. |
| `WZAP_CHATWOOT_MESSAGE_DELETE` | `false` | Sincroniza revogações nos dois sentidos. |
| `WZAP_CHATWOOT_IMPORT_DB_URL` | vazio | URI do Postgres do Chatwoot para o import; vazia desliga o import. |
| `WZAP_CHATWOOT_IMPORT_PLACEHOLDER` | `false` | Mensagem sem conteúdo vira `(mídia não importada)` em vez de ser pulada. |

| Método e rota | Corpo/Resposta |
| --- | --- |
| `PUT /instances/{id}/chatwoot` | Configuração do conector → `200`; validação falha → `422`. O `token` é aceito só na escrita e nunca volta nas respostas. |
| `GET /instances/{id}/chatwoot` | `200` com a configuração (sem o `token`) e a `webhook_url`. |
| `POST /instances/{id}/chatwoot/import` | Import manual → `202` com `{"imported":N}`, onde N conta mensagens importadas (contatos não entram na conta). |
| `POST /chatwoot/webhook/{id}` | Webhook aberto por desenho (sem auth), responde corpo de bot. |

O espelho cobre texto, mídias (imagem, vídeo, áudio, documento e figurinha
via anexo), contato simples e em lista, localização, listas, reactions,
botões interativos (incluindo PIX), pedidos, produtos e anúncios (com a
miniatura anexada quando os bytes vêm no evento). Enquetes, chamadas, avisos
de protocolo e reactions criptografadas não têm equivalente em texto e são
puladas com `warn`, sem derrubar o worker.

O import ordena por telefone+tempo, deduplica por `source_id` (`WAID:`,
compartilhado com o espelho), respeita `days_limit` e dispara em três
gatilhos: automático pós-pareamento (uma vez), manual (`POST .../import`) e
cron de 30 min com janela de 6 h (limpa acumuladores e o cache do conector);
falha num lote retorna a contagem parcial junto com o erro; início e
resultado avisam na conversa operacional em pt-BR.

Riscos operacionais: token guardado em claro mas nunca ecoado (só escrita),
webhook aberto por desenho com busca de anexos sob allowlist SSRF
(só `http`/`https`, no máximo 3 redirects, metadados/link-local sempre
bloqueados, IP privado/loopback só para o host do Chatwoot configurado) e
SQL direto no banco do Chatwoot (frágil a upgrades — módulo isolado,
desligável pela URI; `display_id` com retry limitado em conflito de unicidade
contra escritas do Rails).

## Eventos

O serviço publica em um stream JetStream (nome em `WZAP_NATS_STREAM`, padrão
`WZAP`, subjects `wzap.>`, retenção em `WZAP_EVENT_RETENTION_DAYS`). Cada
instância tem seis subjects:

- `wzap.instances.{instance_id}.message` — mensagem recebida.
- `wzap.instances.{instance_id}.receipt` — recibo de entrega/leitura/reprodução.
- `wzap.instances.{instance_id}.connection` — mudança de estado da instância.
- `wzap.instances.{instance_id}.message.status` — status de envio.
- `wzap.instances.{instance_id}.message.edit` — edição de mensagem recebida.
- `wzap.instances.{instance_id}.message.delete` — remoção/revoke de mensagem recebida.

O webhook por instância assina quatro tipos (`message`, `receipt`,
`connection`, `message.status`); edição/remoção trafegam só no NATS.

Todo evento carrega o mesmo envelope versionado (`event_version: 1`);
`event_id` é estável e enviado como `Nats-Msg-Id` para deduplicação no broker
(janela de 2 min). A publicação passa pelo outbox (`event_outbox`) e por um
relay com retry: o evento sobrevive a reinícios e a indisponibilidades do
broker. A entrega é **at-least-once**: deduplique pelo `event_id` e trate
reentregas como idempotentes.

```json
{
  "event_id": "0f8c3f1e-...",
  "event_version": 1,
  "type": "message",
  "instance_id": "3b1d...",
  "occurred_at": "2026-09-13T18:00:00.123456789Z",
  "payload": { "from_jid": "...", "chat_jid": "...", "is_group": false,
               "message_id": "...", "timestamp": "...", "type": "text",
               "text": "olá" }
}
```

Payloads por `type`:

- `message`: `from_jid`, `chat_jid`, `is_group`, `message_id`, `timestamp`,
  `type` (tipo do WhatsApp: `text`, `image`, `video`, `audio`, `document`,
  `location` etc.), `text` quando houver. Com mídia armazenada:
  `media: {media_id, mimetype, filename?, size, url, expires_at}`; sem mídia
  (acima do limite, indisponível ou falha no download):
  `media_omitted: {reason}`.
- `receipt`: `message_ids` (ids WhatsApp das mensagens atualizadas), `status`
  (`delivered`, `read` ou `played`), `chat_jid`, `timestamp`. Correlacione com
  `whatsapp_id` dos eventos `message.status`.
- `connection`: `status` (`disconnected`, `pairing`, `connected`, `error`),
  `whatsapp_jid` quando conhecido e `reason` em falha.
- `message.status`: `message_id` (UUID do wzap), `status` (`sent` ou `failed`),
  `whatsapp_id` quando enviada e `error` quando falha.

## Operação local

O `docker-compose.yml` da raiz sobe o serviço em `127.0.0.1:8081`, com volume
próprio e dependências Postgres/NATS.

```bash
# na raiz do repositório
docker compose up -d wzap

# prontidão
curl 127.0.0.1:8081/readyz
# {"data":{"status":"ready","checks":{"migrations":"ok","nats":"ok","postgres":"ok"}}}
```

O compose define `WZAP_API_KEY=${WZAP_API_KEY:-dev-wzap-token}`
(sobrescreva definindo a variável no ambiente ou no `.env` da raiz),
`WZAP_DATABASE_URL` apontando para o banco `wzap` dentro da rede,
`WZAP_NATS_URL=nats://nats:4222`, `WZAP_PUBLIC_URL=http://127.0.0.1:8081` e
`WZAP_AUTO_MIGRATE=true`. O `WZAP_PUBLIC_URL` é o que faz `media.url` dos
eventos apontar para um endereço alcançável pelas aplicações consumidoras;
ajuste-o no compose ou com um override quando necessário. Para o seed do
admin inicial, defina também `WZAP_ADMIN_EMAIL` e `WZAP_ADMIN_PASSWORD`.

**Banco em volume pré-existente:** o script `docker/postgres-init.sql` cria o
banco de teste automaticamente apenas quando o volume do Postgres é
inicializado do zero. Em um volume existente, crie-o uma única vez:

```bash
docker compose exec postgres createdb -U wzap wzap_test
```

```bash
# smoke test autenticado (rotas na raiz, header apikey:)
curl -sS -X POST 127.0.0.1:8081/instances \
  -H 'apikey: dev-wzap-token' \
  -H 'Content-Type: application/json' \
  -d '{"name":"smoke","external_ref":"smoke-1"}'
# {"data":{"id":"...","status":"disconnected","owner_user_id":"...","instance_api_key":"..."}}

curl -sS -X POST 127.0.0.1:8081/instances/<id>/connect \
  -H 'apikey: dev-wzap-token'
# {"data":{"status":"pairing","qr_code":"2@...","qr_expires_at":"..."}}
```

Encerramento: o serviço trata `SIGINT`/`SIGTERM`, drena as requisições em voo e
só depois para o outbox, a limpeza de mídia, o worker de webhooks e, por
último, o relay — que publica os eventos pendentes. Todo o encerramento
compartilha um limite de 10 s e um segundo sinal o aborta imediatamente.

## Testes

Os testes de integração exigem um Postgres acessível cujo banco termine em
`_test`; sem `WZAP_TEST_DATABASE_URL` eles são pulados. Crie o banco de teste
uma vez:

```bash
docker compose exec postgres createdb -U wzap wzap_test
```

```bash
# suite completa
WZAP_TEST_DATABASE_URL='postgres://wzap:secret@127.0.0.1:5432/wzap_test?sslmode=disable' \
  go test ./... -count=1

go vet ./...
golangci-lint run
go build ./...
gofmt -l .          # saída vazia = formatado
```

No CI (`.github/workflows/ci.yml`) rodam `go vet`,
`golangci-lint` v2.13.2, `go test ./...` e `go build ./...` com um Postgres de
serviço.

## Checklist manual de pareamento e envio real

Depende de um aparelho humano com WhatsApp; execute uma vez por release ou
quando o fluxo de sessão mudar. Ainda **não executado** (pendente de número de
teste):

- [ ] Subir o serviço (`docker compose up -d wzap`) e confirmar `/readyz` com
      `postgres`, `migrations` e `nats` em `ok`.
- [ ] Criar uma instância de teste e iniciar o pareamento; renderizar a string
      `qr_code` em um gerador de QR e escanear com o aparelho.
- [ ] Confirmar `GET .../status` em `connected` com `whatsapp_jid` e o evento
      `...connection` publicado.
- [ ] Enviar texto, localização, contato e uma mídia para um número de teste;
      confirmar `202`, estado `sent` e os recibos `delivered`/`read`.
- [ ] Receber uma mensagem de texto no aparelho e confirmar o evento
      `...message` com o payload.
- [ ] Receber uma mídia, confirmar o evento com `media` e baixar por
      `GET /media/{id}` com a key (e confirmar `401` sem credencial).
- [ ] Desconectar a instância, confirmar que não há reconexão e que
      `DELETE` responde `204` com `404` nas operações seguintes.

## Referências

- Specs e decisões: `openspec/changes/wzap-foundation/` (proposal, design,
  specs por capability) e `openspec/changes/wzap-product/` (contrato produto:
  contas, api keys, webhooks, manager; quebras marcadas **BREAKING**).
- Licenças e trechos reaproveitados: `THIRD_PARTY_NOTICES.md`.
