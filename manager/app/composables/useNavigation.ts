import type { NavigationMenuItem } from '@nuxt/ui'

// Single source for sidebar navigation and command-palette entries. Later
// tasks append their screens here (instances, pairing, keys/webhook,
// accounts) with scope flags; the layout renders whatever this returns, so
// actions outside the account scope stay absent instead of disabled.
export function useNavigation() {
  const { t } = useI18n()

  const items = computed<NavigationMenuItem[]>(() => [
    {
      label: t('nav.overview'),
      icon: 'i-lucide-house',
      to: '/'
    }
  ])

  return { items }
}
