import type { NavigationMenuItem } from '@nuxt/ui'

// Single source for sidebar navigation and command-palette entries. Later
// tasks append their screens here (pairing, sending) with scope flags; the
// layout renders whatever this returns, so actions outside the account scope
// stay absent instead of disabled.
export function useNavigation() {
  const { t } = useI18n()
  const { isAdmin } = useAuth()

  const items = computed<NavigationMenuItem[]>(() => [
    {
      label: t('nav.overview'),
      icon: 'i-lucide-house',
      to: '/'
    },
    {
      label: t('nav.instances'),
      icon: 'i-lucide-smartphone',
      to: '/instances'
    },
    // Account management is admin-only: user sessions never see this entry.
    ...(isAdmin.value
      ? [{
          label: t('nav.accounts'),
          icon: 'i-lucide-users',
          to: '/accounts'
        }]
      : [])
  ])

  return { items }
}
