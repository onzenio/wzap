<script setup lang="ts">
import type { MessageStatus } from '~/types/api'

// Colored dot + label for the outbound delivery state. The API reports
// queued | sending | sent | failed, with sent and failed terminal.
const props = defineProps<{
  status: MessageStatus
}>()

const { t } = useI18n()

const color = computed(() => {
  switch (props.status) {
    case 'sent':
      return 'success'
    case 'failed':
      return 'error'
    case 'sending':
      return 'info'
    default:
      return 'neutral'
  }
})
</script>

<template>
  <UBadge :color="color" variant="subtle">
    {{ t(`instances.messages.status.${status}`) }}
  </UBadge>
</template>
