## Context

Ver `proposal.md` (Why) para a motivação. Estado atual que molda o desenho:

- HTTP em stdlib `ServeMux` (`internal/httpapi/server.go`): `/healthz`,
  `/readyz` públicos; sub-mux autenticado montado em `/api/v1/` com `Auth`
  (Bearer) + `envelopeFallback`; handlers resolvem dono via `PathValue("id")`.
- Persistência Postgres via `internal/storage/postgres/` + migrations goose
  embutidas; uma migration (`00001_init.sql`) até aqui.
- Eventos: envelope versionado (`event_version: 1`, `event_id` estável) via
  outbox transacional + relay NATS; entrega at-least-once.
- Binário estático distroless (`CGO_ENABLED=0`), deploy único via compose;
  runtime suportado de 1 réplica; locks em memória.
- Referências estudadas, usadas para **recriar** (não copiar): `apime` local
  (users/roles/RBAC, `TokenHash` e `WebhookURL` por instância, seed de admin,
  normalizer de eventos); `asternic/wuzapi` (ergonomia de webhook: `x-hmac-signature`,
  retry configurável, QR base64, CRUD admin, `Subscribe` por webhook);
  `tulir/whatsmeow` oficial (semântica dos campos de evento; versão pinada no
  `go.mod`); template `nuxt-ui-templates/dashboard` (base do manager).
  Transporte diverge das três (aqui `apikey:`, sem prefixo).

## Goals / Non-Goals

**Goals:**

- Dois planos de credencial convivendo nas mesmas rotas: sessão de usuário
  (humanos, RBAC) e apikey (máquinas, escopo global/instância).
- Manager como artefato do mesmo binário, sem runtime extra.
- Migração de contrato explícita e total (sem período de dupla aceitação),
  aproveitando a janela pré-release.

**Non-Goals:**

- Nenhum requisito novo de infra (sem Redis, sem réplicas, sem fila externa
  além do NATS existente).
- Nenhuma mudança na semântica de pareamento/envio; o envelope NATS ganha
  apenas o campo `event` cru (aditivo).

## Decisions

### Dual auth por resolução ordenada

Uma credencial por request, resolvida nesta ordem: cookie de sessão JWT →
header `apikey:` igual à global → hash no banco (instance key). O resultado é
um escopo (`global` | `user+role` | `instance`) injetado no contexto; handlers
e RBAC autorizam sobre o escopo, nunca sobre o transporte.
Alternativas: middlewares separados por plano (duplicaria montagem de rotas);
rotas distintas para manager e máquinas (duplicaria a API). A resolução única
mantém uma matriz de autorização testável.

### Rotas na raiz com mux externo + sub-mux autenticado

Registro público exato (`/healthz`, `/readyz`, `/swagger/`, `/manager/` e
estáticos) no mux externo; sub-mux da API montado em `/` atrás do auth. O
`ServeMux` resolve pelo padrão mais específico, sem colisão (`/instances`,
`/media`, `/users`, `/auth` não chocam com as públicas). Path desconhecido sem
credencial responde `401` antes de `404` (documentado, não testado como erro).
Alternativa: prefixar tudo sob `/api` sem versão — recusada pelo dono
(sem prefixo algum).

### Users, bcrypt e JWT em cookie httpOnly

Tabela `users` (email único, `password_hash` bcrypt, `role` validada) +
sessão JWT assinada (claims: sub, role, exp curto) em cookie `HttpOnly`,
`SameSite=Lax`, `Secure` quando em HTTPS. Sem refresh tokens nesta versão
(login novamente ao expirar). `role` validada em código (`admin|user`), não
string livre.
Alternativas: sessão server-side (revogação imediata, mas estado e lookup por
request); JWT em localStorage (mais simples, vulnerável a XSS). Cookie httpOnly
é o equilíbrio para mesma origem.

### Seed do admin via env quando vazio

Se `users` está vazia e `WZAP_ADMIN_EMAIL`/`WZAP_ADMIN_PASSWORD` configuradas,
o boot cria o admin (bcrypt) antes de servir; sem as vars, nada é criado.
Alternativas: seed fixo estilo apime (credencial conhecida — recusado por
segurança); subcomando CLI (explícito, porém fricção no compose); wizard de
primeiro acesso (melhor UX, mais trabalho — possível v2).

### Ownership imutável sem transferência

`instances.owner_user_id` NOT NULL, definido na criação (sessão criadora; key
global sem sessão → admin mais antigo, sobrescrevível por parâmetro só
global/admin) e nunca atualizado. Delete de user com instâncias → `409`.
Alternativas: transferência (recusada pelo dono); cascata (perda silenciosa —
recusada); owner nulo para criações máquina (quebraria "exatamente um dono").

### Instance key: aleatória, hash sha256, uso único na exibição

32 bytes aleatórios, armazena só o hash; comparação em tempo constante;
devolvida em claro somente na criação/rotação. Sem prefixo identificador (por
decisão do dono; o lookup resolve global primeiro, depois banco).
Alternativas: prefixo `wzap_inst_` (diagnóstico mais fácil — recusado);
JWT por instância (stateless, mas revogação exigiria denylist).

### Webhook como worker dedicado sobre o outbox de eventos

Reuso do envelope NATS acrescido do `event` cru: cada evento de tipo assinado
gera uma entrega HTTP (POST JSON com envelope + `event` whatsmeow serializado
e credencial no header `apikey:`). Blobs acima de `MaxMediaBytes` são cortados
do cru com omissão marcada; a mídia segue pela URL do envelope. Assinatura de
tipos (`events`, padrão todos) por webhook, no estilo do `Subscribe` do wuzapi
mas com nossos nomes minúsculos. Worker próprio com backoff exponencial
limitado e dead-letter em log; instância sem key não tem entregas. Sem fila
nova: estado de tentativas vive no processo, coerente com 1 réplica.
Normalização recriada a partir do normalizer do apime; estrutura de entrega
inspirada no wuzapi. Sem copiar código das referências.
Alternativas: fila durável no Postgres (redelivery infinito — recusado);
reuso do relay NATS (acoplaria broker a HTTP — recusado); pass-through cru
puro estilo wuzapi sem envelope (quebraria o contrato versionado — recusado).

### Apikey pura no webhook, sem HMAC (divergência de wuzapi e apime)

O wuzapi assina com `x-hmac-signature` e o apime usa `WebhookSecret` separado;
aqui a entrega carrega a instance key em claro no header `apikey:` e o
recebedor compara. Decisão consciente do dono pela simplicidade: sem
integridade criptográfica, a confidencialidade depende de HTTPS (obrigatório
fora de loopback; HTTP só em loopback para dev). Risco registrado abaixo.
Rotação da key troca a credencial das entregas seguintes imediatamente.

### Manager: Nuxt UI (template dashboard) estático embutido

Fork integral pontual do template `ui/dashboard` em `manager/` (scaffolding
via create; sem acompanhamento de upstream, sem notices por peça): remove
`server/api` e mocks, adiciona login,
guarda de rotas, cliente da API (sessão via cookie; mesma origem, sem CORS) e
telas (overview, instâncias, pareamento+QR, keys, webhook, envio de teste,
mensagens, contas). `@nuxtjs/i18n` com locale EN desde já; `qr-code-styling`
via npm; `app.baseURL: '/manager/'`; `nuxi generate` → `.output/public` →
`go:embed` no Go, com fallback SPA para `index.html`. Docker multi-stage
(Node/pnpm → Go); Node só no build.
Alternativas: templ+htmx (sem toolchain — recusado por UX); SPA separada com
deploy próprio (quebra o "binário único" — recusado).

### Swagger via swaggo com frescura no CI

Anotações nos handlers + `swag init --parseInternal` (obrigatório: código vive
em `internal/`); gerados commitados em `docs/`; CI roda `swag init` +
`git diff --exit-code`. `securityDefinition: apiKey` no header `apikey`;
UI pública em `/swagger/*` via `http-swagger` (embed, compatível com
distroless). Limitação assumida: Swagger 2.0, não OpenAPI 3.
Alternativas: OpenAPI 3 manual + Scalar (sem codegen — recusado pelo dono);
Huma v2 (refactor total dos handlers — recusado).

### Referências como estudo, recriar sem copiar

Regra permanente do produto: whatsmeow manda na semântica dos campos, apime
nos padrões de normalização, wuzapi na ergonomia de webhook, template na UI —
estudar função e lógica prontas e recriar na nossa arquitetura, sem
copiar-colar. Sem notices novos em `THIRD_PARTY_NOTICES.md` (nada copiado).
Desvio de referência exige motivo registrado (como o apikey-sem-HMAC acima).
Alternativa: copiar-adaptar com notices por peça (recusada pelo dono).

### whatsmeow pinado com upgrade manual

Versão fixa no `go.mod`; upgrade só em change dedicada com `go build`, testes
e checklist de pareamento (o upstream quebra API com frequência — alerta do
próprio wuzapi). Nunca `latest` automático.
Alternativas: acompanhar latest (retrabalho imprevisível — recusado).

### Cotas como COUNT + config (sem limiter)

Teto global (`WZAP_MAX_INSTANCES`, `0`=∞) + cota por usuário (default
`WZAP_DEFAULT_USER_INSTANCE_QUOTA`, `0`=∞, override do admin); verificação por
contagem no Postgres na criação; exceder → `403 quota_exceeded`; admin
bypassa. Sem estado em memória e sem token bucket: cota é autorização, não
rate limit.
Alternativas: bucket em memória (para req/min — fora de escopo); Redis
(fora do desenho).

## Risks / Trade-offs

- [Risk] Superfícies públicas (`/swagger`, `/manager`) expõem a forma da API →
  Mitigação: documentado e aceito pelo dono; dados seguem exigindo credencial.
- [Risk] JWT sem revogação imediata: demitir/desabilitar conta não derruba
  sessões vigentes até `exp` → Mitigação: expiração curta; sem flag de disable
  nesta versão (só remoção, que bloqueia re-login); revogação server-side é v2.
- [Risk] `401`-antes-de-`404` em paths desconhecidos sem credencial muda a
  semântica de descoberta → Mitigação: documentado; rotas públicas seguem
  exatas e sem auth.
- [Risk] Seed via env deixa senha em variável/gu compose → Mitigação: mesma
  classe de segredo da key global; documentar rotação pós-instalação (troca de
  senha no manager).
- [Risk] Manager aumenta CI/build (Node, pnpm, Nuxt) e pode flakar →
  Mitigação: lockfile commitado, versão Node pinada no Dockerfile/CI, build do
  manager como job separado com cache.
- [Risk] Apikey em claro em cada entrega: quem intercepta um POST se passa
  pelo wzap e detém poder total sobre a instância (inclui delete) → Mitigação:
  HTTPS obrigatório fora de loopback; header dedicado (nunca em URL); risco
  aceito e documentado pelo dono; segredo separado é v2.
- [Risk] `event` cru varia com o upstream (whatsmeow adiciona/remove campos) →
  Mitigação: versão pinada + contrato estável no envelope; cru é melhor-esforço,
  nunca parseado pelo nosso código.
- [Risk] Fork pontual do template diverge do upstream (sem fixes futuros) →
  Mitigação: decisão consciente; template é base visual, telas são nossas;
  avaliar sync manual só se necessário.
- [Trade-off] Swagger 2.0 em vez de OpenAPI 3 (limitação do swaggo) em troca de
  geração automática a partir do código.

## Migration Plan

Pré-release: migração direta, sem dupla aceitação e sem rollback de contrato
(rollback = redeploy da versão anterior; dados novos — users, keys, webhook —
são aditivos e ignorados pelo código antigo, exceto `owner_user_id NOT NULL`,
que exige backfill na própria migration para o admin seed/first-admin… ver
nota). Passos:

1. Aplicar migration (users, owner com backfill, key hashes, webhook, cotas).
2. Configurar `WZAP_API_KEY` (renomeada), seed de admin e cotas; remover
   `WZAP_SERVICE_TOKEN` (ignorada se presente).
3. Atualizar consumidores: header `apikey:`, paths sem prefixo, `media.url`.
4. Rotacionar keys das instâncias antigas (backfill via rotate) e entregar aos
   clientes; reconfigurar webhooks (URL + tipos assinados).
5. Rebuild do manager embutido acompanha o binário (sem passo separado).

Nota: `owner_user_id NOT NULL` em base com instâncias e sem users exige dono
provisório — a migration atribui ao primeiro admin criado pelo seed no mesmo
boot; em `migrate` sem seed, a migration falha orientando criar o admin
(env + `serve` uma vez) antes. Detalhe de implementação na task.

Rollback: redeploy anterior + restore do banco se a migration já rodou (down
migrations seguem o padrão do projeto, se houver; senão, restore).

## Open Questions

Nenhuma.
