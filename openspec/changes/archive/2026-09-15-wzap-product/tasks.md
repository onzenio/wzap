## 1. Persistência e configuração

- [x] 1.1 Criar migration com users, owner das instâncias (com backfill), hashes de instance key, webhook e cotas, verificando aplicação em banco limpo e em banco legado com instâncias
- [x] 1.2 Implementar repositórios de users e credenciais (keys, webhook, cotas), verificando testes de integração contra Postgres
- [x] 1.3 Renomear `WZAP_SERVICE_TOKEN` para `WZAP_API_KEY` e adicionar envs de seed admin, cotas e JWT, verificando testes do parser (válido, ausente, inválido)

## 2. Autenticação e autorização

- [x] 2.1 Implementar login/logout com JWT em cookie httpOnly e hash bcrypt de senhas, verificando cenários `200`, `401` e sessão invalidada
- [x] 2.2 Implementar seed do admin no boot quando sem users, verificando criação com envs e ausência sem envs
- [x] 2.3 Implementar resolução dual de credencial (sessão → global → instance key) com escopo no contexto, verificando testes da matriz global/user/instância
- [x] 2.4 Aplicar RBAC e ownership em todas as rotas (admin tudo, user só as próprias, key só a própria instância), verificando `403` nos cruzamentos e `404` em inexistente
- [x] 2.5 Mover as rotas para a raiz sem prefixo com sub-mux autenticado, verificando `404` em `/api/v1/*` e públicas exatas sem auth

## 3. Instâncias, keys e cotas

- [x] 3.1 Emitir dono + instance key (uma vez) na criação, verificando `201` com key e não-reexibição posterior
- [x] 3.2 Implementar rotação e revogação de instance key (só global/admin), verificando invalidação imediata e backfill de instância antiga
- [x] 3.3 Implementar cotas global e por usuário com bypass do admin, verificando `403 quota_exceeded` e contagem por estado
- [x] 3.4 Implementar CRUD de users com bloqueio `409` para dono com instâncias, verificando cenários de gerência e remoção

## 4. Webhooks

- [x] 4.1 Implementar configuração de webhook por instância (`url`, `enabled`, `events`), verificando validação `422` (URL, HTTPS fora de loopback, tipos) e habilitação
- [x] 4.2 Implementar worker de entrega com envelope + `event` cru (corte de blobs acima do limite) e header `apikey:`, verificando header e corpo contra receptor de teste
- [x] 4.3 Implementar retry exponencial limitado com dead-letter em log, verificando sucesso tardio e descarte após o limite

## 5. Documentação Swagger

- [x] 5.1 Anotar handlers e gerar docs com `swag init --parseInternal`, verificando `doc.json` com rotas e securityDefinition apikey
- [x] 5.2 Servir `/swagger/*` pública com http-swagger e adicionar check de frescura no CI, verificando `200` no index e falha com docs defasadas

## 6. Manager web

- [x] 6.1 Criar app Nuxt por fork integral pontual do template `ui/dashboard` (sem `server/api`/mocks, com i18n EN e cliente da API, sem acompanhamento de upstream), verificando `pnpm build` e login funcional contra o Go
- [x] 6.2 Implementar telas de instâncias (lista, criação com key única, edição, remoção com confirmação digitada), verificando fluxos nas visões admin e user
- [x] 6.3 Implementar pareamento com QR renderizado e polling, verificando reemissão ao expirar e transição a `connected`
- [x] 6.4 Implementar telas de keys (rotate/revogação), webhook (URL, tipos assinados, habilitação) e contas/cotas, verificando persistência e escopos
- [x] 6.5 Implementar envio de teste (texto/mídia) com acompanhamento e consulta de mensagens, verificando `sent`/`failed`
- [x] 6.6 Embutir build estático no binário (`baseURL /manager`, fallback SPA) com Docker multi-stage, verificando `/manager` servido e refresh em rota interna

## 7. Integração e verificação final

- [x] 7.1 Atualizar README, AGENTS.md e specs com o contrato novo e marcas **BREAKING**, verificando revisão dos artefatos (sem notices novos: referências recriadas, não copiadas)
- [x] 7.2 Executar gates completos (`go vet`, `golangci-lint`, `go test ./...` com Postgres, `go build`, build do manager), verificando saída limpa
- [ ] 7.3 Executar verificação local completa pelo manager (seed admin, conta cliente, instância, QR real, envio real, webhook recebido), verificando evidências e cobrindo o 8.4 pendente do foundation
