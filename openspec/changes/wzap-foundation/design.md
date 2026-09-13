## Context

Ver `proposal.md` para motivação e escopo. Este design assume o estudo do
`open-apime/apime` documentado em `docs/research/apime` (mapa em
`brainstorm.md`) como referência de domínio, sem fork de código: o wzap nasce
próprio, reaproveitando funções pontuais sob licença MIT.

Restrições que moldam a solução: serviço interno multi-instância consumido
somente pelo `backend/`; stack local já tem Postgres e NATS JetStream; a
integração com o Laravel (consumo dos eventos) fica para uma change futura.

## Goals / Non-Goals

**Goals:**

- Contrato REST de comandos estável e observável, coberto pelas specs.
- Entrega de eventos com durabilidade real (sem perda silenciosa).
- Sessões WhatsApp sobrevivendo a reinícios do serviço.
- Fronteiras de código testáveis sem depender do protocolo real.
- Operação local de um comando (`docker compose up`) com healthcheck embutido.

**Non-Goals:**

- Escala horizontal e lock distribuído (uma réplica por desenho).
- Paridade de recursos com o apime (dashboard, grupos, newsletters, stories).
- Consumidor Laravel, deploy de produção e observabilidade de plataforma.
- Garantia de exactly-once no envio (o desenho é at-least-once, documentado).

## Decisions

### D1. Serviço único modular em Go com stdlib HTTP (Abordagem A)

Um binário `wzap` com pacotes de domínio (`httpapi`, `instance`, `message`,
`session`, `events`, `media`, `storage`, `config`, `observability`) e wiring
manual. HTTP com `net/http` + middleware próprio; logs com `log/slog`.

- **Por quê**: menor superfície de dependências, sem framework HTTP pesado para
  um contrato pequeno; interfaces no lado consumidor seguem o idioma de Go e
  permitem fakes nos testes.
- **Alternativas**: processo por instância ou API stateless + workers (adiadas
  como evolução; a complexidade operacional não se paga na v1); Gin (herdado do
  apime, porém desnecessário para o tamanho do contrato).

### D2. Postgres como única persistência; migrations com goose

Banco `wzap` no Postgres existente; sessões do whatsmeow no mesmo banco
via sqlstore. Migrations SQL embutidas, aplicadas no boot quando habilitado.

- **Por quê**: um só banco reduz partes móveis, permite transações do outbox e
  o `FOR UPDATE SKIP LOCKED`; sqlstore evita arquivos de sessão por instância.
- **Alinhamento com a base**: decisão do humano (2026-09-13) de manter o núcleo
  de dados com os nomes/shape das migrations finais do apime (`message_queue`,
  `contacts`, `idempotency_keys` com `idempotency_key`/`request_hash`), já que
  ele é a base do serviço; a similaridade facilita portar consultas e funções.
- **Alternativas**: SQLite (duplicaria implementações, sem lock entre processos,
  e CGO); runner de migrations caseiro (frágil, como observado no apime).

### D3. Eventos via NATS JetStream com outbox transacional

Stream `WZAP` com subjects `wzap.instances.{id}.{connection|message|receipt|message.status}`,
envelope versionado e publicação via tabela `event_outbox` + relay com retry.

- **Por quê**: durabilidade e replay reais; o outbox garante que uma queda do
  broker ou do processo não perca eventos gerados.
- **Alternativas**: publicação direta (perde eventos em indisponibilidade);
  webhooks HTTP (exigiria endpoint no Laravel, retry/DLQ próprios e não foi a
  escolha do stack).
- **Sem `reply_to` na v1**: o recorte de inbound não carrega a fonte de
  reply/citação; quoting fica para change futura (decisão de 2026-09-13).
- **Cap de mídia**: `MediaLength` do proto é checado antes do download e o
  stream é limitado a `MaxMediaBytes`, para o limite valer memória/banda e não
  só armazenamento (decisão de 2026-09-13).

### D4. Envio assíncrono com outbox e recuperação de interrupção

`202` + `message_id`; workers consomem `queued` com `FOR UPDATE SKIP LOCKED` e
lock por instância; estados `queued → sending → sent|failed`; retries com
backoff para falhas transitórias; mensagens presas em `sending` são retomadas.

- **Por quê**: o envio real pode levar segundos (protocolo, presença, retries);
  desacoplar mantém a API responsiva e o estado auditável.
- **Alternativas**: envio síncrono na requisição (timeouts longos no Laravel e
  pior recuperação de falhas).

### D5. Idempotência no aceite (semântica portada do apime)

`Idempotency-Key` opcional; replay devolve a resposta original; 409 em
concorrência; 422 para mesmo key com conteúdo diferente; TTL de 24 h.

- **Por quê**: a semântica já validada no apime (com testes) resolve retries de
  rede do cliente sem duplicar envios.
- **Alternativas**: deduplicação por conteúdo (ambígua); idempotência só do lado
  do Laravel (não protege reenvio HTTP).

### D6. Autenticação por token único de serviço

Bearer estático por ambiente, comparação constant-time, porta publicada apenas
na rede interna (dev: `127.0.0.1`). O wzap não conhece usuários, roles nem
Account.

- **Por quê**: um único consumidor, isolamento de rede; minimiza superfície e
  remove todo o subsistema de auth/dashboard do apime.
- **Alternativas**: mTLS (complexidade de PKI sem ganho imediato); JWT/API
  tokens por usuário (necessário só se um dia houver múltiplos consumidores).

### D7. Instância agnóstica de conta, com `external_ref`

O wzap identifica instâncias por `id` e aceita `external_ref` opcional e único;
o vínculo com Account/Client vive no Laravel.

- **Por quê**: mantém a fronteira de domínio limpa e evita duplicar o modelo
  multi-account no serviço de mensageria.
- **Alternativas**: armazenar `account_id`/`client_id` como FK (acopla o wzap ao
  domínio fiscal sem necessidade observável).

### D8. Mídia em volume local com TTL e download autenticado

Download automático de recebidas até `WZAP_MAX_MEDIA_BYTES`; upload multipart
para envio; identificadores opacos; limpeza periódica; download exige token.

- **Por quê**: simples, sem dependência de objeto storage na v1; o Laravel busca
  a mídia logo após o evento.
- **Alternativas**: S3/MinIO (adiado; a interface de armazenamento permite
  trocar depois); expor pública como o apime (falha de segurança observada).

### D9. whatsmeow upstream, isolado atrás de interface

O pacote `session` encapsula a biblioteca; os serviços dependem de interfaces,
e a versão fica pinada.

- **Por quê**: base oficial e mantida; o fork do apime não tem governança nossa.
- **Alternativas**: fork `Whalabs/whatsmeow` (permite copiar sem adaptação, mas
  cria dependência de um fork de um dia); vendorizar o fork (custo de manter
  patches de protocolo).

### D10. Empacotamento distroless com healthcheck embutido

`CGO_ENABLED=0` (sem SQLite, dependências puras), imagem distroless, não-root,
e subcomando `wzap healthcheck` para o healthcheck do compose.

- **Por quê**: binário estático dispensa shell e libc; o subcomando resolve o
  healthcheck sem `curl`/`wget` na imagem.
- **Alternativas**: imagem Debian/Alpine com wget (maior superfície); healthcheck
  externo ao binário (dependência desnecessária).

### D11. Reuso pontual do apime com atribuição

Portamos lógica já testada onde o desenho bate: semântica de idempotência,
resolver de JID com 9º dígito BR, math de humanização e validação de payload de
mídia. O aviso MIT e a origem ficam em `wzap/THIRD_PARTY_NOTICES.md`.

- **Por quê**: reduz risco de reescrever lógica sensível, cumprindo a licença.
- **Alternativas**: copiar o repositório inteiro (rejeitado no brainstorm) ou
  não reusar nada (retrabalho desnecessário).

### D12. QR como string

O pareamento devolve a string do QR e a expiração; o cliente renderiza.

- **Por quê**: evita dependência de geração de imagem no serviço; o front do
  Laravel já tem bibliotecas de QR.
- **Alternativas**: PNG via `go-qrcode` como no dashboard do apime (só se
  houver necessidade observável futura).

## Risks / Trade-offs

- [Risk] Réplica única com locks em memória: um crash afeta todas as instâncias.
  → Mitigação: restauração automática no boot com concorrência limitada, lock
  por instância e caminho de evolução para processo por instância (D1).
- [Risk] At-least-once no envio pode duplicar mensagem em crash dentro da janela
  `sending`. → Mitigação: janela de recuperação curta e configurável, estado e
  possível duplicata observáveis na consulta de status.
- [Risk] Banimento/restrição da conta pelo WhatsApp. → Mitigação: humanização
  opcional, backoff, estado `error` sem reconexão automática e registro de
  motivo; operação manual documentada.
- [Risk] Mudanças de API no whatsmeow upstream. → Mitigação: versão pinada e
  isolamento atrás de interfaces (D9), com adaptação em um único pacote.
- [Risk] Evolução de schema dos eventos quebrar o consumidor. → Mitigação:
  `event_version` no envelope, mudanças aditivas dentro da mesma versão e
  documentação do contrato no README.
- [Risk] TTL de mídia curto para o ritmo do consumidor. → Mitigação: TTL
  configurável, `expires_at` exposto e recomendação de consumo imediato.
- [Risk] Vazamento do token de serviço. → Mitigação: rede interna, segredo por
  ambiente, comparação constant-time e nunca registrar o token.
- [Risk] Outbox de eventos crescer sem limite se o broker ficar fora. →
  Mitigação: relay com retry, limpeza de registros publicados e alerta de
  readiness quando o broker estiver indisponível.
- [Risk] Postgres único ponto de falha. → Mitigação: aceito na v1 (interno);
  pool limitado, readiness sinaliza e a change futura de produção decide HA.
- [Risk] Fluxo de pareamento por QR depende de humano no loop. → Mitigação:
  contrato simples de string + polling e checklist manual documentado.
- [Risk] Reuso MIT sem atribuição adequada. → Mitigação: `THIRD_PARTY_NOTICES.md`
  com licença, origem e lista de trechos reaproveitados (D11).

## Migration Plan

- Serviço novo, sem migração de dados existentes. Implantação local: criar o
  banco `wzap`, subir o serviço no compose e validar `/readyz`.
- O Postgres atual já tem volume inicializado: o banco novo é criado uma vez por
  comando (`createdb`) e também por script de init para volumes futuros.
- Rollback: remover o serviço e o volume; `backend/` e `frontend/` não são
  afetados nesta change.
- O consumidor Laravel (envio e JetStream) entra em change própria.

## Open Questions

- Como o Laravel vai consumir o JetStream (cliente PHP direto ou bridge) — não
  altera o contrato do wzap e fica para a change do consumidor.
- Métricas de plataforma (Prometheus ou equivalente) — adiado; a v1 expõe logs e
  readiness.
- Alvo de produção e requisitos de HA — adiado; o desenho atual é de uma réplica.
