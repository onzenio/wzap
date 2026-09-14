## Context

Ver `proposal.md` (Why) para a motivação. Estado atual que molda o desenho:

- `session.EventSink` tem só `OnMessage/OnReceipt/OnConnection` (`internal/session/session.go`); whatsmeow não trata `ProtocolMessage`/history-sync, não expõe LID e pareia só por QR.
- Envio é assíncrono (`message.Service.Enqueue` + outbox workers com retry/status); inbound tem `MediaDownload` + `media.Storage`; JID resolver com regra BR; locks em memória (`instancelock`, 1 réplica); relay NATS single-worker (produtor único).
- Contrato-alvo é o do `wzap-product` (raiz + `apikey:`) — dependência explícita.
- Referência (Evolution, `research/evolution-api/.../chatwoot`, ~3385 linhas) usada para **recriar**, sem copiar: DTO de 17 campos, `set`/`find`/webhook aberto, ~40 métodos (espelho, contatos com cache+lock, import via SQL direto com chunks/CTE/dedup `WAID:`).

## Goals / Non-Goals

**Goals:**

- Espelho em tempo real sem degradar a sessão e sem bifurcar o envio (webhook reusa `Enqueue`).
- Paridade funcional com o Evolution nos fluxos (não no código).
- Módulo de import isolado, desligável por ausência de URI.

**Non-Goals:**

- Nenhuma mudança em pareamento/envio fora do necessário (pairing-code, delete, mark-read, history feed).
- Nenhuma infra nova além do NATS/Postgres existentes; sem multi-réplica.

## Decisions

### Worker como consumer JetStream durável in-process

O espelho consome subjects da instância via durable `wzap-chatwoot` em vez de hook síncrono no `Runtime` ou canal interno. Alternativas: hook inline (trava a sessão quando o Chatwoot lentifica — recusado); canal interno (perde evento em queda — recusado). Consumidor é leitura, não segundo relay: quem publica segue único.

### Webhook reusa o outbox de envio

Resposta do atendente entra por `Enqueue` (texto/mídia via `MediaID` após baixar `data_url` para o `media.Storage`). Alternativa: `session.Send` direto estilo Evolution (perderia retry, `message.status`, idempotência — recusado).

### Reuso máximo da casa

`MediaDownload`+`media.Storage` (sem re-download), JID resolver (conceito BR nas buscas), `instancelock` (locks por remetente), cleaner (sem purga própria — espelho do Evolution). Alternativa: port fiel com lógica própria (duplicaria envio/mídia/locks — recusado).

### Cliente Chatwoot hand-rolled mínimo

Só os endpoints mapeados (contacts/filter/search, inboxes, conversations, messages, contact_merge, `update_last_seen`, `access_tokens`). Alternativa: SDK Go (não há oficial — recusado).

### Extensão de sessão nesta change

`OnMessageEdit/OnMessageDelete`, `DeleteMessage`, `MarkRead`, pairing-code e feed history-sync entram aqui (sem eles cai edit/delete-sync, delete reverso, read-sync, import e `init:<number>`). Regra LID condicional: mapeia se a lib expuser alt-JID, senão JID + `warn`.

### Tabelas e retenção fiéis

`chatwoot_configs` (17 campos) + `chatwoot_messages` (correlação); sem purga, apaga no `DELETE` da instância — igual ao Evolution (verificado: sem TTL lá; único cron é o `syncLostMessages`).

### Deps novas pinadas

`nyaruka/phonenumbers` (formato internacional) + `golang.org/x/image` (thumbnail de ads). Sem elas cai fidelidade de grupos e ads.

### Divergências conscientes do Evolution

Webhook aberto mantido (secret é v2); token em claro (cifragem é v2); bot em pt-BR fixo (sem i18n); `tags` com single-insert (o original executa 2×); sem `try connected` no-op.

## Risks / Trade-offs

- [Risk] History-sync da lib pinada pode não expor o necessário → Mitigação: verificar no build; feed adapta aos eventos reais, detalhes na task.
- [Risk] SQL direto frágil a upgrades do Chatwoot → Mitigação: módulo isolado, guard por URI, documentado.
- [Risk] Token em claro + webhook aberto → Mitigação: riscos operacionais documentados; secret/cifragem são v2.
- [Risk] Consumer durável novo no processo → Mitigação: só leitura; shutdown junto ao relay (ordem preservada).
- [Risk] Dependência de `wzap-product` (contrato) → Mitigação: change bloqueada até o contrato aterrissar; paths/auth referenciam-no.
- [Risk] Contato operacional `123456` mágico mantido por paridade → Mitigação: constante nomeada + docs.
- [Trade-off] Sem purga da correlação em troca de replies antigos sempre resolvíveis (linhas pequenas).

## Migration Plan

Pré-release, sem dupla aceitação:

1. `wzap-product` aplicado primeiro (contrato).
2. Aplicar migration (2 tabelas novas, aditivas).
3. Configurar envs `WZAP_CHATWOOT_*`; por instância, `set` + `auto_create` (revisar `webhook_url` no Chatwoot).
4. Consumer/cron sobem com o binário (sem passo separado).

Rollback: redeploy anterior; tabelas novas ignoradas pelo código antigo; remover consumer durable órfão se renomeado.

## Open Questions

- Níveis exatos dos eventos de history-sync na lib pinada (verificação no build; não muda specs nem abordagem).
