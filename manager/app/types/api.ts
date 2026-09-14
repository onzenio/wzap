// Shared shapes of the Go API contract. Success answers { data }, failures
// answer { error: { code, message } }; every response echoes X-Request-Id.

// Success envelope wrapping every 2xx JSON payload.
export interface ApiEnvelope<T> {
  data: T
}

// Error envelope returned for 4xx/5xx JSON responses.
export interface ApiErrorEnvelope {
  error: {
    code: string
    message: string
  }
}

// Account role assigned at creation; admin sees everything, user sees only
// the instances the account owns.
export type AccountRole = 'admin' | 'user'

// Identity answered by POST /auth/login and GET /auth/me.
export interface SessionUser {
  id: string
  email: string
  role: AccountRole
}

// Vision scope derived from the role: admin gets the operator-wide vision,
// user is restricted to the account's own instances.
export type VisionScope = 'global' | 'instance'

// Connection state reported by the API for an instance.
export type InstanceStatus = 'disconnected' | 'pairing' | 'connected' | 'error'

// Instance as answered by GET /instances and GET /instances/{id}. The shape
// mirrors instanceResponse in internal/httpapi/instances.go: it carries the
// owner and webhook configuration on every read but never the instance API
// key, which is returned in clear exactly once by the create and rotate
// answers below.
export interface Instance {
  id: string
  name: string
  external_ref: string
  owner_user_id: string | null
  webhook_url: string | null
  webhook_enabled: boolean
  webhook_events: string[]
  status: InstanceStatus
  whatsapp_jid: string
  last_error: string
  last_connected_at: string | null
  created_at: string
  updated_at: string
}

// One page of GET /instances with the opaque cursor of the next page, empty
// on the last page.
export interface InstanceListPage {
  items: Instance[]
  next_cursor: string
}

// POST /instances answer: the instance plus its one-time plaintext key. No
// other read route returns this shape.
export interface CreatedInstance extends Instance {
  instance_api_key: string
}

// POST /instances/{id}/apikey/rotate answer: the instance id with its fresh
// one-time plaintext key.
export interface RotatedInstanceKey {
  id: string
  instance_api_key: string
}

// POST /instances/{id}/connect and GET /instances/{id}/qr answer: the
// resulting status plus, while pairing, the QR payload and its validity.
// Mirrors connectResponse in internal/httpapi/connection.go: qr_code and
// qr_expires_at are absent when the instance is already connected, and GET
// qr answers 409 instead when there is no QR to scan.
export interface ConnectResult {
  status: InstanceStatus
  qr_code?: string
  qr_expires_at?: string | null
}

// GET /instances/{id}/status answer: the connection state of an instance.
// Mirrors statusResponse in internal/httpapi/connection.go.
export interface ConnectionStatus {
  status: InstanceStatus
  whatsapp_jid: string
  last_error: string
  last_connected_at: string | null
}

// Payload sent to POST /instances. owner_user_id stays unset here: only the
// global scope and admin sessions may send it, and the console always creates
// for the signed-in account (user sessions own what they create, admin
// creations without owner fall back to the oldest admin server-side).
export interface CreateInstanceInput {
  name: string
  external_ref?: string
}

// Payload sent to PATCH /instances/{id}. An explicit empty external_ref
// clears the stored reference.
export interface UpdateInstanceInput {
  name: string
  external_ref: string
}

// Account as answered by GET /users (admin scope only). Used to resolve the
// owner column of the instance list.
export interface AccountUser {
  id: string
  email: string
  role: AccountRole
  instance_quota: number
}
