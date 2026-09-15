<script setup lang="ts">
import type { Instance } from '~/types/api'

defineProps<{
  instance: Instance
}>()
</script>

<template>
  <span v-if="instance.whatsapp_jid" class="font-mono text-sm text-muted">
    {{ instance.whatsapp_jid }}
  </span>
</template>
