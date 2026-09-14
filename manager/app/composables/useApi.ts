import type { ApiEnvelope, ApiErrorEnvelope } from '~/types/api'

// Normalized error thrown for non-2xx API answers: HTTP status plus the
// envelope code/message when the body carries one.
export class ApiError extends Error {
  status: number
  code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

// Typed fetch for the Go API. The browser always talks to the same origin
// (session cookie travels automatically), so no apikey header is attached
// here; machine callers outside the browser use the apikey header instead.
// Success envelopes are unwrapped to their data field.
export function useApi() {
  const config = useRuntimeConfig()
  const baseURL = (config.public.apiBaseUrl as string) || undefined

  const raw = $fetch.create({
    baseURL,
    credentials: 'include',
    headers: {
      Accept: 'application/json'
    },
    onResponseError({ response }) {
      const body = response._data as ApiErrorEnvelope | undefined
      const code = body?.error?.code ?? 'request_failed'
      const message = body?.error?.message ?? `Request failed with status ${response.status}`
      throw new ApiError(response.status, code, message)
    }
  })

  async function api<T>(path: string, options?: Parameters<typeof raw>[1]): Promise<T> {
    const envelope = await raw<ApiEnvelope<T>>(path, options)
    return envelope.data
  }

  return { api, raw }
}
