<script setup lang="ts">
defineProps<{
  collapsed?: boolean
}>()

const { t } = useI18n()
</script>

<template>
  <NuxtLink to="/" class="flex items-center gap-2.5 px-1 py-2 min-w-0">
    <span class="grid size-8 shrink-0 place-items-center rounded-lg bg-primary text-inverted font-bold">
      w
    </span>
    <span v-if="!collapsed" class="min-w-0 leading-tight">
      <span class="block truncate font-semibold text-highlighted">{{ t('app.name') }}</span>
      <span class="block truncate text-xs text-muted">{{ t('app.tagline') }}</span>
    </span>
  </NuxtLink>
</template>
