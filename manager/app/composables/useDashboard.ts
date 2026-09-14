import { createSharedComposable } from '@vueuse/core'

// Shell-level shortcuts. Later tasks register their own keys here next to
// the overview shortcut; keeping them in one place avoids collisions.
const _useDashboard = () => {
  const router = useRouter()

  defineShortcuts({
    'g-h': () => router.push('/')
  })
}

export const useDashboard = createSharedComposable(_useDashboard)
