# Brainstorm — wzap-product

Descoberta que antecede o proposal. Registra perguntas, opções e decisões; a
spec oficial nasce no `openspec-propose`.

## Contexto

O `wzap-foundation` entregou o gateway headless (68/69 tasks; só o checklist
manual 8.4 pendente). A pergunta inicial era estreita — "página de gerência de
sessões? swagger?" — e durante o brainstorming revelou um pivô de
posicionamento: de serviço interno para **produto instalável**, onde quem
instala cria múltiplas instâncias, revende instâncias e integra sistemas
externos, com experiência inspirada em Evolution API, WuzAPI e apime.

Referências estudadas: `swaggo/swag` (docs), Evolution API (global apikey +
tokens por instância + Manager), htmx/templ (stacks Go), template
`nuxt-ui-templates/dashboard` (base do manager) e cópia local do apime em
`/home/obsidian/dev/research/apime` (users/roles/RBAC, `TokenHash` e
`WebhookURL` por instância, seed de admin). O repo/docs do WuzAPI não foi
localizado (fetch 404) — link pendente com o dono para alinhar o estilo dos
payloads de evento.

## Perguntas e decisões (sessão 1 — docs e manager)

| # | Pergunta | Decisão |
|---|----------|---------|
| 1 | Abordagem da API docs | swaggo + http-swagger (Swagger 2.0 gerado) |
| 2 | Escopo da página de sessões | Completa + envio de teste |
| 3 | Auth de docs e admin | Docs pública, admin com token na página |
| 4 | Quem usa o manager | Produto instalável, revenda de instâncias, estilo Evolution |
| 5 | Individualizar/vender | Tokens por instância (depois: apikeys) |
| 6 | Ciclo de vida do token | Híbrido: nasce junto, exibe uma vez, com rotate/revogação |
| 7 | Escopo do token de instância | Controle total da própria instância, menos gerenciar o próprio token |
| 8 | Acesso ao console | Global vê tudo, instância vê a sua (depois: login por conta) |
| 9 | Eventos para o cliente | Webhooks já neste design |
| 10 | Arquitetura do console | Embutido no binário Go |
| 11 | Eventos no webhook | Todos os 4 tipos, estilo WuzAPI (link pendente) |
| 12 | Retry do webhook | Exponencial com limite + dead-letter |
| 13 | Segredo do webhook | Reutilizar a instance key no HMAC (diverge do apime) |
| 14 | Token → apikey | Header `apikey:`, troca direta, keys aleatórias puras |
| 15 | Env var | Renomear `WZAP_SERVICE_TOKEN` → `WZAP_API_KEY` |
| 16 | Stack do manager | Nuxt + Nuxt UI (template `ui/dashboard`), build estático embutido |
| 17 | Prefixo `/api/v1` | Eliminado; rotas na raiz |

## Perguntas e decisões (sessão 2 — grilling + domain-modeling)

| # | Pergunta | Decisão |
|---|----------|---------|
| Q1 | "Sessão" ou "instância"? | Ambos soltos, sem padronização |
| Q2 | Nomes dos atores | Estilo apime (pesquisado no repo local) |
| Q3 | Nome da superfície | Manager, servido em `/manager` |
| Q4 | White-label | Fora desta versão |
| Q5 | Client deleta a própria instância | Pode, com confirmação digitada no manager |
| Q7 | Modelo apime completo? | Sim: User + role + login + ownership + RBAC; máquinas seguem em keys (cenário Chatwoot resolvido em dois planos) |
| Q8 | Segredo webhook separado? | Não; manter reuso da instance key |
| Q9 | Idioma do manager | EN agora, `@nuxtjs/i18n` desde já |
| Q10 | Bootstrap do admin | Seed via env no boot se tabela vazia |
| Q11 | Sessão do manager | JWT em cookie httpOnly, estilo apime |
| Q12 | Criação de contas | Só admin cria; sem registro público |
| Q13 | Deletar dono com instâncias | Bloquear `409`; sem transferência, sem cascata |
| Q14 | Poder do user comum | Cria instâncias e gerencia tudo nas próprias |
| Q15 | Global key continua? | Sim; humano=login, máquina=keys |
| Q16 | Roles | Fixas: `admin`, `user` (validadas) |
| Q17 | Key independente do dono | Sim; só rotate/revogação afetam |
| Q18 | Transferência de dono | Não; dono imutável |
| Q19 | Dual auth nas rotas | Sim; sessão OU apikey, escopos equivalentes |
| Q20–21 | Rate limiting / i18n | "Completo" redefinido como cota de nº de instâncias; i18n desde já |
| Q26–27 | Escopo das cotas / admin | Global + por usuário; admin global pode tudo (bypassa) |
| Q28 | Código de cota excedida | `403 quota_exceeded` (corrigido de 429: cota ≠ retry) |
| Q29 | msg/min e req/min | Fora; gancho para v2 |
| Q30–31 | Defaults / contagem | `0` = ilimitado; toda instância existente conta |

## Perguntas e decisões (sessão 3 — grilling das referências)

Diretriz do dono: sempre usar as referências em vez de criar do zero —
dashboard template, eventos do apime, wuzapi e whatsmeow oficial. Estudo real:
`API.md` do wuzapi (tipos PascalCase, `x-hmac-signature`, retry simples,
`Subscribe`, QR base64), normalizer do apime (snake_case, reaction/presence/
contact_update, confirmação de JID) e `types/events` do whatsmeow (fonte da
semântica). Regra-mãe fechada: referências servem para **recriar**, não para
copiar — sem notices novos.

| # | Pergunta | Decisão |
|---|----------|---------|
| R1 | Cru estilo WuzAPI x envelope | Envelope + campo `event` cru juntos |
| R2 | Nomes de tipos | Nossos 4 minúsculos (sem BREAKING nos atuais) |
| R3 | Cobertura de eventos | Manter os 4 + gancho (tabela de mapeamento pronta) |
| R4a | Header de assinatura | `apikey:` pura, sem HMAC (risco aceito + HTTPS) |
| R5 | Reuso do template | Fork integral pontual, sem acompanhar upstream |
| R6 | Regra de precedência | Referências como estudo de função/lógica pronta; recriar |
| R-sub | Assinatura de tipos | Lista estilo wuzapi, valores nossos, padrão todos — já agora |
| R-retry | Retry simples x exponencial | Exponencial ao longo de horas (wuzapi só estrutura) |
| R-https | HTTP permitido? | HTTPS fora, HTTP só em loopback |
| R-teto | Teto dos blobs no cru | Reusa `MaxMediaBytes`, marca omissão |
| R-pin | Versão do whatsmeow | Pinada + upgrade manual em change dedicada |
| R-reuso | Lista de reuso | Mínimo seguro: normalizer apime + retry/QR/admin wuzapi (recriados) |
| R-fork | Escopo do fork | Shell + remove mocks (`server/api`, demos) |
| R-gloss | Novos termos | evento cru, assinatura de webhook, peça recriada |

## Tensões resolvidas

- **Fronteira "sem usuários" revogada**: era fundadora do wzap (AGENTS.md,
  README, openspec). O modelo apime exige contas — revogação registrada como
  decisão consciente, com atualização dos artefatos na task 7.1.
- **Dois planos de credencial**: humanos (login/sessão/RBAC) e máquinas (keys);
  o cenário "integrar com Chatwoot" validou a separação.
- **"Rate limit completo" redefinido**: o dono queria só cota de número de
  instâncias — simplificou o desenho (COUNT + config, sem limiter).
- **Prefixo e Bearer removidos sem transição**: janela pré-release assumida
  como oportunidade para troca direta.
- **Apikey sem HMAC**: diverge de wuzapi e apime por simplicidade; a key circula
  em cada entrega (poder total) — mitigado com HTTPS obrigatório e documentado
  como risco aceito, com segredo separado em v2.
- **Cru integral com teto**: o "cru estilo WuzAPI" esbarrou no tamanho (outbox/
  NATS/POST) — resolvido cortando blobs acima de `MaxMediaBytes` e mantendo o
  restante, com a mídia via URL do envelope.
