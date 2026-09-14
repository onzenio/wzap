<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import type { Instance } from '~/types/api'

// Destructive confirmation: the delete button stays disabled until the typed
// text matches the instance name exactly, so an accidental click removes
// nothing.
const props = defineProps<{
  instance: Instance
}>()

const emit = defineEmits<{
  deleted: [id: string]
}>()

const { t } = useI18n()
const { deleteInstance } = useInstances()

const open = defineModel<boolean>('open', { default: false })

const typedName = ref('')
const pending = ref(false)
const failure = ref<string | null>(null)

const matches = computed(() => typedName.value === props.instance.name)

watch(open, (value) => {
  if (value) {
    typedName.value = ''
    pending.value = false
    failure.value = null
  }
})

async function onDelete() {
  if (pending.value || !matches.value) {
    return
  }
  pending.value = true
  failure.value = null
  try {
    await deleteInstance(props.instance.id)
    forgetInstanceKeySeen(props.instance.id)
    open.value = false
    emit('deleted', props.instance.id)
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('instances.delete.failed')
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <UModal v-model:open="open" :title="t('instances.delete.title')" :description="t('instances.delete.body', { name: instance.name })">
    <template #body>
      <div class="flex flex-col gap-4">
        <UAlert color="error" variant="subtle" :title="t('instances.delete.warning')" />

        <UFormField :label="t('instances.delete.confirmLabel', { name: instance.name })" name="confirm-name">
          <UInput v-model="typedName" class="w-full" autocomplete="off" />
        </UFormField>

        <UAlert
          v-if="failure"
          color="error"
          variant="subtle"
          :title="failure"
        />
      </div>
    </template>

    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton
          color="neutral"
          variant="ghost"
          :label="t('common.cancel')"
          @click="open = false"
        />
        <UButton
          color="error"
          :disabled="!matches"
          :loading="pending"
          :label="t('instances.delete.submit')"
          @click="onDelete"
        />
      </div>
    </template>
  </UModal>
</template>
