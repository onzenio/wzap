import type {
  AcceptedMessage,
  MessageListPage,
  NumberCheckResult,
  OutboundMessage,
  SendMediaInput
} from '~/types/api'

// Typed client for the number and message endpoints. Every call runs in the
// session scope (the wzap_session cookie travels automatically); failures
// throw ApiError with the envelope code so screens can render scoped
// messages. Sends carry a fresh Idempotency-Key per click so a retried
// request replays instead of enqueueing the same message twice.
export function useMessages() {
  const { api } = useApi()

  // POST /instances/{id}/numbers/check answers 200 with the resolution: a
  // malformed or unknown number reports exists false instead of an error. An
  // instance without a usable session answers 503. The check never enqueues.
  async function checkNumber(instanceId: string, phone: string): Promise<NumberCheckResult> {
    return await api<NumberCheckResult>(`/instances/${instanceId}/numbers/check`, {
      method: 'POST',
      body: { phone }
    })
  }

  // POST /instances/{id}/messages/text answers 202 with the queued message
  // id. A disconnected instance answers 409 and an unknown number 422, both
  // without enqueueing anything.
  async function sendText(instanceId: string, to: string, text: string): Promise<AcceptedMessage> {
    return await api<AcceptedMessage>(`/instances/${instanceId}/messages/text`, {
      method: 'POST',
      headers: { 'Idempotency-Key': newIdempotencyKey() },
      body: { to, text }
    })
  }

  // POST /instances/{id}/messages/media answers 202 with the queued message
  // id. The declared type must match the file content type or the server
  // answers 422 before storing or enqueueing anything.
  async function sendMedia(instanceId: string, input: SendMediaInput): Promise<AcceptedMessage> {
    const form = new FormData()
    form.append('to', input.to)
    form.append('type', input.type)
    if (input.caption) {
      form.append('caption', input.caption)
    }
    if (input.filename) {
      form.append('filename', input.filename)
    }
    if (input.ptt) {
      form.append('ptt', 'true')
    }
    form.append('file', input.file, input.file.name)
    return await api<AcceptedMessage>(`/instances/${instanceId}/messages/media`, {
      method: 'POST',
      headers: { 'Idempotency-Key': newIdempotencyKey() },
      body: form
    })
  }

  // GET /instances/{id}/messages/{message_id} reports the current delivery
  // state; the test-send card polls it until sent or failed.
  async function getMessage(instanceId: string, messageId: string): Promise<OutboundMessage> {
    return await api<OutboundMessage>(`/instances/${instanceId}/messages/${messageId}`)
  }

  // GET /instances/{id}/messages answers one page with its next cursor. An
  // invalid cursor answers 400.
  async function listMessages(instanceId: string, cursor?: string): Promise<MessageListPage> {
    const query = cursor ? { cursor } : {}
    return await api<MessageListPage>(`/instances/${instanceId}/messages`, { query })
  }

  return {
    checkNumber,
    sendText,
    sendMedia,
    getMessage,
    listMessages
  }
}

// One fresh key per send click. The server scopes keys per instance for 24h;
// reusing a key across different payloads would answer 422, so keys are
// never reused here.
function newIdempotencyKey(): string {
  try {
    return crypto.randomUUID()
  } catch {
    return `${Date.now()}-${Math.floor(Math.random() * 0x100000000).toString(16)}`
  }
}
