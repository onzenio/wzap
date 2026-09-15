<script setup lang="ts">
import type { AccountUser } from '~/types/api'

// Row actions for the accounts table. The cell only emits: the page keeps
// openQuota / openDelete (and all API logic), mapping edit-quota -> openQuota
// and remove -> openDelete.
defineProps<{
  user: AccountUser
}>()

defineEmits<{
  (e: 'edit-quota' | 'remove', user: AccountUser): void
}>()

const { t } = useI18n()
</script>

<template>
  <div class="flex gap-2">
    <UButton
      color="neutral"
      variant="soft"
      icon="i-lucide-pencil"
      :label="t('accounts.quota.edit')"
      @click="$emit('edit-quota', user)"
    />
    <UButton
      color="error"
      variant="soft"
      icon="i-lucide-trash-2"
      :label="t('accounts.delete.action')"
      @click="$emit('remove', user)"
    />
  </div>
</template>
