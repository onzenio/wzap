## Why

Aplicações precisam falar com seus contatos por WhatsApp sem carregar o estado
e a complexidade do protocolo. Um gateway independente e multi-instância isola
essa responsabilidade, oferece entrega durável de eventos e permite que cada
consumidor evolua sem incorporar o motor WhatsApp.

## What Changes

- Novo serviço Go independente (módulo `wzap`), multi-instância e consumido por
  aplicações confiáveis com token de serviço.
- API REST de comandos sob `/api/v1`: instâncias (criar, parear por QR, status,
  reconectar, desconectar, remover) e envio de mensagens (texto, imagem, vídeo,
  áudio/PTT, documento, localização, contato) com idempotência por
  `Idempotency-Key` e resposta assíncrona `202` com `message_id`.
- Validação e normalização de número com a regra do 9º dígito brasileiro antes
  do enfileiramento.
- Eventos inbound (mensagens, recibos e mudanças de conexão) publicados no stream
  NATS `WZAP` por instância, com envelope versionado e publicação via outbox
  (at-least-once).
- Mídia recebida baixada automaticamente (limite e TTL configuráveis) e mídia de
  envio recebida por multipart; download autenticado e limpeza periódica.
- Persistência em Postgres (banco `wzap`), incluindo sessões do whatsmeow
  via sqlstore; sem SQLite e sem Redis.
- Runtime: binário estático em imagem distroless, healthcheck embutido,
  `/healthz` e `/readyz`; serviço `wzap` no `docker-compose.yml` da raiz e CI
  próprio com lint e testes.

## Capabilities

### New Capabilities

- `wzap-instances`: ciclo de vida de uma instância WhatsApp (criação, pareamento
  por QR, status, reconexão automática, desconexão e remoção), incluindo
  restauração de sessões no boot.
- `wzap-outbound-messaging`: envio assíncrono e idempotente de texto, mídia,
  localização e contato, com estado consultável e recuperação de falhas.
- `wzap-inbound-events`: contrato observável dos eventos de mensagens recebidas,
  recibos e conexão publicados no NATS JetStream.
- `wzap-media`: armazenamento temporário, integridade e download autenticado das
  mídias recebidas e enviadas.
- `wzap-operations`: configuração por ambiente, saúde/readiness, migrações,
  empacotamento e execução do serviço no ambiente local.

### Modified Capabilities

- Nenhuma. `openspec/specs/` está vazio; todas as capabilities acima são novas.

## Impact

- Raiz do repositório: código-fonte, migrations, Dockerfile, README,
  `THIRD_PARTY_NOTICES` (reaproveitamento MIT do estudo em `docs/research/apime`)
  e configuração de lint.
- `docker-compose.yml` (raiz): novo serviço `wzap`, volume `wzap-media` e banco
  `wzap` no Postgres existente.
- CI: workflow próprio com lint, testes e build.
- Implementações consumidoras não fazem parte deste repositório.
- Sem mudanças **BREAKING** para os serviços existentes.

## Out-of-Scope

- Dashboard embutido, usuários/roles/API tokens e webhooks HTTP.
- Grupos, newsletters, stories, broadcast lists e presença avançada.
- SQLite, Redis, múltiplas réplicas e lock distribuído.
- Sentry, métricas Prometheus e rate limiting.
- Implementações consumidoras e qualquer deploy de produção.
