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
