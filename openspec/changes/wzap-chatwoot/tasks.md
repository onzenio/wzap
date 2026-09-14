## 1. Persistência e configuração

- [x] 1.1 Criar migration com `chatwoot_configs` (17 campos) e `chatwoot_messages` (correlação, sem purga, FK com delete em cascata), verificando aplicação em banco limpo via `postgres.Migrate` em teste
- [x] 1.2 Implementar repositórios (config por instância, correlação wa_key↔IDs, delete por instância), verificando testes de integração contra Postgres com `postgrestest`
- [x] 1.3 Adicionar envs `WZAP_CHATWOOT_*` (enabled, import URI, operational contact, message read/delete) ao parser, verificando testes (válido, ausente, inválido)

## 2. Extensão de sessão/whatsmeow

- [x] 2.1 Tratar `ProtocolMessage` (REVOKE/EDIT) no dispatch com `OnMessageEdit/OnMessageDelete` no sink, verificando testes com eventos sintetizados e fakes atualizados
- [x] 2.2 Implementar `DeleteMessage` e `MarkRead` na sessão + whatsmeow, verificando testes contra fake e chamada à lib
- [x] 2.3 Implementar pareamento por código (para `init:<number>`), verificando fluxo com código simulado
- [x] 2.4 Implementar feed de history-sync (notificações de progresso + lotes + contatos) por instância, verificando níveis reais da lib pinada no build e acumuladores isolados por instância

## 3. Cliente e contatos/conversas

- [x] 3.1 Implementar cliente HTTP mínimo do Chatwoot (contacts, inboxes, conversations, messages, merge, filter/search, `update_last_seen`) sobre `httptest` fake, verificando paths, headers e corpos contra o fake
- [x] 3.2 Implementar resolução de conversa com cache + `instancelock` por remetente (reuso open/pending/reopen), verificando convergência sob concorrência em teste
- [x] 3.3 Implementar contatos com regra BR (variantes 9º dígito, merge, grupos, foto/nome) e labels best-effort via `pgx` (pula sem URI + `warn`), verificando contra fakes
- [ ] 3.4 Adicionar deps pinadas (`phonenumbers`, `x/image`), verificando `go build ./...` limpo

## 4. Espelho WA→Chatwoot

- [ ] 4.1 Implementar consumer JetStream durável in-process com dedup (`event_id`, `source_id`), verificando reentrega idempotente contra NATS de teste
- [ ] 4.2 Implementar mapeamento de conteúdo (texto, 6 mídias via `media.Storage`, contato, localização, listas, reaction, PIX, ads com thumbnail, markdown, grupos, replies via correlação), verificando cada tipo contra Chatwoot fake
- [ ] 4.3 Implementar edit/delete/read-sync e avisos da conversa operacional em pt-BR (conexão, QR com imagem+pairing-code, throttle 30s), verificando cenários contra fakes
- [ ] 4.4 Publicar novos subjects de edit/delete via outbox, verificando envelope versionado e consumo pelo worker

## 5. Webhook Chatwoot→WA

- [ ] 5.1 Implementar `POST /chatwoot/webhook/{id}` aberto com filtros (private, `message_updated`, eco `WAID:`, bot), verificando `200` com corpo de bot nos descartes
- [ ] 5.2 Implementar texto com assinatura e anexos (`data_url` → `media.Storage` → `Enqueue`), quoted via correlação e nota privada em falha, verificando mensagens enfileiradas e `sent`
- [ ] 5.3 Implementar delete reverso, template e mark-read, verificando efeitos via sessão fake
- [ ] 5.4 Implementar `set`/`find`/`import` sob o contrato produto (raiz + `apikey:`) com validação `422`, verificando matriz de auth e envelopes

## 6. Import histórico

- [ ] 6.1 Implementar pool `pgx` do Postgres do Chatwoot com guard por URI, verificando inércia total sem URI
- [ ] 6.2 Implementar import de contatos e mensagens (ordem, dedup `WAID:`, lotes, CTE de FKs, `days_limit`, placeholders), verificando idempotência em reexecução contra Postgres fake/real de teste
- [ ] 6.3 Implementar gatilhos (auto pós-pareamento, manual, cron 30min `syncLostMessages`) com avisos no bot, verificando cada gatilho em teste

## 7. Fiação e verificação final

- [ ] 7.1 Fiar tudo em `serve()` (repos, worker/consumer, cron, rotas) preservando ordem de shutdown, verificando boot com `WZAP_CHATWOOT_ENABLED` on/off
- [ ] 7.2 Executar gates (`go vet`, `golangci-lint`, `go test ./...` com Postgres, `go build`, `gofmt -l` vazio), verificando saída limpa
- [ ] 7.3 Atualizar README/AGENTS.md e validar a change (`openspec validate`), verificando `openspec status` completo
