<script setup lang="ts">
import type { Instance } from '~/types/api'

defineProps<{
  instance: Instance
}>()
</script>

<template>
  <div class="min-w-0 flex-1">
    <p class="truncate font-medium text-highlighted">
      {{ instance.name }}
    </p>
    <p v-if="instance.external_ref" class="truncate text-sm text-muted">
      {{ instance.external_ref }}
    </p>
  </div>
</template>
