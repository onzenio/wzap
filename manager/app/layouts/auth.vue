<script setup lang="ts">
const { t } = useI18n()
</script>

<template>
  <div class="flex min-h-svh flex-col items-center justify-center gap-6 p-4">
    <AppBrand />
    <UCard class="w-full max-w-sm">
      <slot />
    </UCard>
    <p class="text-sm text-muted">
      {{ t('app.tagline') }}
    </p>
  </div>
</template>
