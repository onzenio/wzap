import type { SessionUser, VisionScope } from '~/types/api'

// Session state for the manager console. Login mints the wzap_session
// httpOnly cookie on the Go side; the browser resends it automatically, so
// this store only keeps the identity returned by /auth/me and derives the
// vision scope from the role: admin sees everything (global), user sees only
// the account's own instances (instance).
export function useAuth() {
  const user = useState<SessionUser | null>('auth-user', () => null)
  const ready = useState<boolean>('auth-ready', () => false)

  const isAuthenticated = computed(() => user.value !== null)
  const isAdmin = computed(() => user.value?.role === 'admin')
  const scope = computed<VisionScope | null>(() => {
    if (!user.value) {
      return null
    }
    return user.value.role === 'admin' ? 'global' : 'instance'
  })

  const { api, raw } = useApi()

  async function refresh(): Promise<SessionUser | null> {
    try {
      user.value = await api<SessionUser>('/auth/me')
    } catch {
      user.value = null
    } finally {
      ready.value = true
    }
    return user.value
  }

  async function login(email: string, password: string): Promise<SessionUser> {
    user.value = await api<SessionUser>('/auth/login', {
      method: 'POST',
      body: { email, password }
    })
    ready.value = true
    return user.value
  }

  async function logout(): Promise<void> {
    try {
      await raw('/auth/logout', { method: 'POST' })
    } catch {
      // The local session is dropped either way.
    } finally {
      user.value = null
      await navigateTo('/login')
    }
  }

  return { user, ready, isAuthenticated, isAdmin, scope, refresh, login, logout }
}
