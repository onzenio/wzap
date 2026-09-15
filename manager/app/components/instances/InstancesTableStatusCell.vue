<script setup lang="ts">
import InstanceStatusBadge from './InstanceStatusBadge.vue'
import type { InstanceStatus } from '~/types/api'

defineProps<{
  status: InstanceStatus
}>()
</script>

<template>
  <InstanceStatusBadge :status="status" />
</template>
