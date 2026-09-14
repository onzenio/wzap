<script setup lang="ts">
const { t } = useI18n()
const route = useRoute()
const open = ref(false)

useDashboard()

const { items } = useNavigation()

const groups = computed(() => [{
  id: 'links',
  label: t('nav.overview'),
  items: items.value.flatMap(item => item.to
    ? [{
        id: String(item.to),
        label: String(item.label),
        suffix: String(item.to),
        onSelect: () => {
          open.value = false
          navigateTo(String(item.to))
        }
      }]
    : [])
}])

watch(() => route.fullPath, () => {
  open.value = false
})
</script>

<template>
  <UDashboardGroup unit="rem">
    <UDashboardSidebar
      id="default"
      v-model:open="open"
      collapsible
      resizable
      class="bg-elevated/25"
      :ui="{ footer: 'lg:border-t lg:border-default' }"
    >
      <template #header="{ collapsed }">
        <AppBrand :collapsed="collapsed" />
      </template>

      <template #default="{ collapsed }">
        <UDashboardSearchButton :collapsed="collapsed" class="bg-transparent ring-default" />

        <UNavigationMenu
          :collapsed="collapsed"
          :items="items"
          orientation="vertical"
          tooltip
          popover
        />

        <UNavigationMenu
          :collapsed="collapsed"
          :items="[{
            label: t('common.docs'),
            icon: 'i-lucide-book-open',
            to: '/swagger/',
            target: '_blank'
          }]"
          orientation="vertical"
          tooltip
          class="mt-auto"
        />
      </template>

      <template #footer="{ collapsed }">
        <UserMenu :collapsed="collapsed" />
      </template>
    </UDashboardSidebar>

    <UDashboardSearch :groups="groups" :placeholder="t('nav.searchPlaceholder')" />

    <slot />
  </UDashboardGroup>
</template>
