## Why

O wzap nasceu como serviço headless interno consumido por backends confiáveis.
A oportunidade agora é transformá-lo em produto instalável: quem instala cria
múltiplas instâncias, revende instâncias a clientes e integra sistemas externos
(estilo Evolution, WuzAPI e apime). Para isso faltam contas de usuário, API keys
por instância, webhooks, um manager web e cotas — além de alinhar o contrato
HTTP ao mercado (sem prefixo de versão, header `apikey`).

## What Changes

- Contas de usuário com `role` fixa (`admin`, `user`), login por email/senha
  com sessão JWT em cookie httpOnly, e seed do primeiro admin via ambiente.
- Ownership de instâncias: toda instância tem um dono imutável; admin vê tudo,
  user comum cria instâncias e gerencia tudo nas próprias.
- **BREAKING**: autenticação de máquina passa de `Authorization: Bearer` para o
  header `apikey:`, com key global (`WZAP_API_KEY`, acesso total) e key por
  instância (controle total da própria instância, menos gerenciar a própria
  key). Rotas aceitam sessão JWT (manager) OU apikey (máquinas).
- **BREAKING**: `WZAP_SERVICE_TOKEN` renomeada para `WZAP_API_KEY`; variável
  antiga é ignorada.
- **BREAKING**: prefixo `/api/v1` eliminado; rotas vivem na raiz (`/instances`,
  `/media/{id}`, …). URLs de mídia em eventos mudam junto (`/media/{id}`).
- Ciclo de vida da instance key: emitida uma única vez na criação, com rotação
  e revogação (só global/sessão admin). Instâncias antigas ganham key via
  rotação.
- Cotas de número de instâncias: teto global via env + cota por usuário com
  override do admin; `0` = ilimitado; exceder responde `403 quota_exceeded`;
  admin global bypassa cotas.
- Webhooks por instância (`url`, `enabled`, lista de tipos): eventos no
  envelope versionado acrescido do `event` cru (whatsmeow serializado, sem
  blobs acima do limite), credencial da instância no header `apikey:` (sem
  HMAC — risco aceito, HTTPS obrigatório fora de loopback), retry exponencial
  limitado e dead-letter em log.
- Manager web em `/manager`: app Nuxt + Nuxt UI (base template `ui/dashboard`,
  EN com `@nuxtjs/i18n` desde já), build estático embutido no binário, login e
  visões admin/cliente (instâncias, QR com polling, keys, webhook, envio de
  teste, mensagens).
- Documentação Swagger (swaggo, `apiKey`) servida em `/swagger/*` pública, com
  check de frescura no CI.

## Out-of-Scope

- White-label do manager (nome/logo por instalação); possível v2.
- Rate limits de msg/min e req/min; só cota de instâncias agora (gancho p/ v2).
- Transferência de ownership; registro público de contas (só admin cria
  users).
- Grupos/newsletters/stories, webhooks globais, multi-réplica: seguem fora como
  na v1.

## Capabilities

### New Capabilities

- `wzap-accounts`: users, roles, login/sessão, ownership e cotas de instâncias.
- `wzap-api-keys`: keys global e por instância, ciclo de vida e matriz de
  escopo (dual auth sessão/key).
- `wzap-webhooks`: configuração e entrega de webhooks por instância.
- `wzap-manager`: comportamento observável do console web em `/manager`.

### Modified Capabilities

- `wzap-operations`: fronteira de auth (header, dual auth, 401/403), envs,
  superfícies servidas (`/manager`, `/swagger`) e bootstrap.
- `wzap-instances`: criação emite key e dono, cotas, endpoints de key.

## Impact

- **BREAKING** no contrato REST (header, paths, env var, corpo de criação) e em
  `media.url` dos eventos; NATS e envelopes inalterados.
- Novas tabelas/colunas (users, owner, key hashes, webhook, cotas) via migration
  goose; fronteira "sem usuários" revogada (AGENTS.md, README, specs).
- Novas dependências Go (swaggo serve, JWT, bcrypt) + toolchain Node (manager) no
  Dockerfile multi-stage e no CI; binário segue único e distroless.
- Referências (dashboard template, apime, wuzapi, whatsmeow) usadas como
  estudo para **recriar** — sem copiar código, sem notices novos; whatsmeow
  segue pinado no `go.mod` com upgrade só em change dedicada.
