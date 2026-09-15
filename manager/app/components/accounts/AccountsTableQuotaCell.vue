<script setup lang="ts">
import type { AccountUser } from '~/types/api'

const props = defineProps<{
  user: AccountUser
}>()

const { t } = useI18n()

// Mirrors quotaLabel in useAccountsTable: quota 0 means unlimited, any other
// quota renders as its number. Keep both in sync.
const label = computed(() => {
  return props.user.instance_quota === 0 ? t('accounts.unlimited') : String(props.user.instance_quota)
})
</script>

<template>
  <span class="text-highlighted">
    {{ label }}
  </span>
</template>
