<script setup lang="ts">
import type { Instance } from '~/types/api'

const props = defineProps<{
  instance: Instance
  email?: string
}>()

const { t } = useI18n()

// Mirrors ownerLabel in useInstancesTable: the resolved account email when
// known, the short id as best-effort fallback, Not set when ownerless.
const label = computed(() => {
  if (props.email) {
    return props.email
  }
  if (!props.instance.owner_user_id) {
    return t('common.notSet')
  }
  return props.instance.owner_user_id.slice(0, 8)
})
</script>

<template>
  <span class="max-w-48 truncate text-highlighted" :title="label">
    {{ label }}
  </span>
</template>
