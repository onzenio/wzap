<script setup lang="ts">
import type { InstanceStatus } from '~/types/api'

// Colored dot + label for the instance connection state. The API reports
// disconnected | pairing | connected | error.
const props = defineProps<{
  status: InstanceStatus
}>()

const { t } = useI18n()

const color = computed(() => {
  switch (props.status) {
    case 'connected':
      return 'success'
    case 'pairing':
      return 'warning'
    case 'error':
      return 'error'
    default:
      return 'neutral'
  }
})
</script>

<template>
  <UBadge :color="color" variant="subtle">
    {{ t(`instances.status.${status}`) }}
  </UBadge>
</template>
