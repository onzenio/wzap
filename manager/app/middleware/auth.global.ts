// Route guard: every page requires a valid session except /login, which
// redirects away once the account is signed in. The session is verified
// once per app load via GET /auth/me; afterwards the cached identity rules.
// Account management is admin-only: user sessions visiting /accounts go back
// to the overview (the sidebar never shows the entry to them).
export default defineNuxtRouteMiddleware(async (to) => {
  const { user, isAdmin, ready, refresh } = useAuth()

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

  if ((to.path === '/accounts' || to.path.startsWith('/accounts/')) && !isAdmin.value) {
    return navigateTo('/')
  }
})
