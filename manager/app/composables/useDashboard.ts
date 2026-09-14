import { createSharedComposable } from '@vueuse/core'

// Shell-level shortcuts. Later tasks register their own keys here next to
// the overview, instances and accounts shortcuts; keeping them in one place
// avoids collisions.
const _useDashboard = () => {
  const router = useRouter()

  defineShortcuts({
    'g-h': () => router.push('/'),
    'g-i': () => router.push('/instances'),
    'g-a': () => router.push('/accounts')
  })
}

export const useDashboard = createSharedComposable(_useDashboard)
