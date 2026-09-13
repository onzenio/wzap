# Brainstorm — wzap-foundation

Descoberta que antecede o proposal. Registra perguntas, opções e decisões; a spec
oficial nasce no `openspec-propose`.

## Contexto

A plataforma (Laravel em `backend/`, Nuxt em `frontend/`) não tem serviço de
mensageria. É preciso um gateway WhatsApp interno, multi-instância, para que o
backend envie e receba mensagens por Account/Client com durabilidade, sem que a
aplicação Laravel carregue o estado do protocolo WhatsApp.

Referência de estudo: cópia local do `open-apime/apime` em `docs/research/apime`
(gateway multi-instância em Go sobre whatsmeow, licença MIT). Foi mapeado por
exploração estruturada (app Go, superfície de API/modelo de dados, runtime/infra).

## Mapa da referência (resumo do estudo)

- ~17k LOC Go não-teste; camadas `cmd → api → service → storage`; composição manual
  em `cmd/api/main.go`; Gin; dashboard SSR embutido; webhooks HMAC; idempotência
  draft-ietf; SQLite e Postgres duplicados; filas memória/Redis.
- Pontos fortes: cobertura ampla do domínio WhatsApp, idempotência madura, JID
  resolver com 9º dígito BR, normalização de 12+ tipos de evento.
- Atritos encontrados: dependência de fork do whatsmeow (`Whalabs`), estado global
  em memória (sem escala horizontal nem lock distribuído), webhooks sem retry/DLQ,
  falhas de segurança (rota de eventos sem guard, mídia pública, CORS `*`), código
  morto, testes rasos e ausência de CI.

## Perguntas e decisões

| # | Pergunta | Decisão |
|---|----------|---------|
| 1 | Papel do serviço | Interno multi-conta: várias instâncias isoladas, consumido pelo backend, auth por token de serviço, sem dashboard de usuários |
| 2 | Estratégia de código | Reimplementar do zero, reaproveitando funções prontas do apime quando fizer sentido |
| 3 | Integração/eventos | REST para comandos + NATS JetStream para eventos inbound |
| 4 | Base do whatsmeow | Upstream `go.mau.fi/whatsmeow` |
| 5 | Nome | `wzap` |
| 6 | Escopo da v1 | Núcleo de mensageria (conexão, envio, recebimento, recibos, validação de número, idempotência, eventos) |
| 7 | Abordagem de arquitetura | A — serviço único modular |

### Alternativas descartadas

- **Fork integral do apime**: herdaria dependência de fork do protocolo, dívida de
  código morto e falhas de segurança; rejeitada em favor de reimplementação com
  reaproveitamento pontual (MIT, com aviso de copyright).
- **Processo por instância** e **API stateless + workers NATS**: complexidade
  operacional desproporcional para a v1; mapeadas como evolução futura.
- **Webhooks HTTP** no lugar de NATS: exigiria retry/DLQ próprios e endpoint no
  Laravel; o stack já tem NATS JetStream.
- **SQLite** e **dashboard embutido**: fora do desenho; Postgres é o único banco.

## Design aprovado (resumo)

- Serviço Go único (`wzap/`, módulo `wzap`), single replica por design.
- REST `/api/v1` com Bearer de serviço único; envelope `{data}`/`{error}`; envio
  assíncrono (`202` + `message_id`) com `Idempotency-Key` (semântica do apime).
- Instâncias: criar, parear via QR, status, reconectar, desconectar, apagar;
  sessão no Postgres via sqlstore do whatsmeow; restauração no boot com jitter.
- Envio: texto, imagem, vídeo, áudio/PTT, documento, localização e contato;
  outbox em Postgres com `FOR UPDATE SKIP LOCKED`, retries, recuperação de crash e
  humanização opcional (math portada do apime).
- Eventos: stream NATS `WZAP`, subjects por instância
  (`connection`, `message`, `receipt`, `message.status`), envelope versionado,
  publicação via outbox (at-least-once).
- Mídia: download automático inbound com limite e TTL, upload multipart no envio,
  download autenticado, cleaner periódico.
- Runtime: `log/slog`, `/healthz`, `/readyz`, binário estático em imagem distroless,
  serviço `wzap` no compose (banco `wzap`, sem Redis), CI com
  golangci-lint + testes.
- Fora de escopo da v1: dashboard, usuários/roles/tokens, grupos/newsletters/
  stories/broadcast, webhooks HTTP, SQLite, múltiplas réplicas, Sentry, rate
  limiting, métricas e a implementação do consumidor Laravel.

## Próximos passos

`openspec-propose` gera proposal/specs/design/tasks; a implementação começa apenas
após novo pedido explícito (workflow de apply), com TDD por task.
