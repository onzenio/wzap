<script setup lang="ts">
// One-time plaintext key display. The key travels in memory only: it is
// shown here exactly once (create or rotate answer) and the API never
// re-exposes it, so the copy reminds the reader to store it now.
defineProps<{
  apiKey: string
}>()

const { t } = useI18n()
const toast = useToast()
const { copy, copied } = useClipboard()

async function copyKey(apiKey: string) {
  await copy(apiKey)
  toast.add({ title: t('instances.key.copied'), color: 'success' })
}
</script>

<template>
  <div class="flex flex-col gap-3">
    <UAlert
      color="warning"
      variant="subtle"
      :title="t('instances.key.shownOnceTitle')"
      :description="t('instances.key.shownOnceBody')"
    />

    <UFormField :label="t('instances.key.label')" name="instance-api-key">
      <div class="flex gap-2">
        <UInput
          :model-value="apiKey"
          readonly
          class="w-full font-mono"
          data-testid="one-time-key"
        />
        <UButton icon="i-lucide-copy" :label="copied ? t('instances.key.copiedShort') : t('instances.key.copy')" @click="copyKey(apiKey)" />
      </div>
    </UFormField>
  </div>
</template>
