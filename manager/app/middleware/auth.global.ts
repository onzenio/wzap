// Route guard: every page requires a valid session except /login, which
// redirects away once the account is signed in. The session is verified
// once per app load via GET /auth/me; afterwards the cached identity rules.
export default defineNuxtRouteMiddleware(async (to) => {
  const { user, ready, refresh } = useAuth()

  if (!ready.value) {
    await refresh()
  }

  if (!user.value) {
    if (to.path !== '/login') {
      return navigateTo('/login')
    }
    return
  }

  if (to.path === '/login') {
    return navigateTo('/')
  }
})
