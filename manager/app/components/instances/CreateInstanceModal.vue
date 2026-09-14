<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import OneTimeKeyDisplay from '~/components/instances/OneTimeKeyDisplay.vue'
import type { CreatedInstance } from '~/types/api'

// Creation dialog: form first, then the created instance with its one-time
// key for copy. Closing the dialog drops the key from memory by design.
const emit = defineEmits<{
  created: [instance: CreatedInstance]
}>()

const { t } = useI18n()
const { createInstance } = useInstances()

const open = defineModel<boolean>('open', { default: false })

const name = ref('')
const externalRef = ref('')
const pending = ref(false)
const failure = ref<string | null>(null)
const created = ref<CreatedInstance | null>(null)

function reset() {
  name.value = ''
  externalRef.value = ''
  pending.value = false
  failure.value = null
  created.value = null
}

watch(open, (value) => {
  if (value) {
    reset()
  }
})

async function onSubmit() {
  if (pending.value) {
    return
  }
  const trimmedName = name.value.trim()
  if (trimmedName === '') {
    failure.value = t('instances.create.nameRequired')
    return
  }
  pending.value = true
  failure.value = null
  try {
    created.value = await createInstance({ name: trimmedName, external_ref: externalRef.value })
    markInstanceKeySeen(created.value.id)
    emit('created', created.value)
  } catch (error) {
    failure.value = error instanceof ApiError ? friendlyCreateError(error) : t('instances.create.failed')
  } finally {
    pending.value = false
  }
}

function friendlyCreateError(error: ApiError): string {
  if (error.status === 403 && error.code === 'quota_exceeded') {
    return t('instances.create.quotaExceeded')
  }
  if (error.status === 409) {
    return t('instances.create.externalRefTaken')
  }
  return error.message
}
</script>

<template>
  <UModal v-model:open="open" :title="created ? t('instances.create.successTitle') : t('instances.create.title')" :description="created ? t('instances.create.successBody') : t('instances.create.body')">
    <template #body>
      <form v-if="!created" class="flex flex-col gap-4" @submit.prevent="onSubmit">
        <UAlert
          v-if="failure"
          color="error"
          variant="subtle"
          :title="failure"
        />

        <UFormField :label="t('instances.fields.name')" name="name" required>
          <UInput
            v-model="name"
            required
            maxlength="255"
            class="w-full"
          />
        </UFormField>

        <UFormField :label="t('instances.fields.externalRef')" :hint="t('instances.fields.externalRefHint')" name="external_ref">
          <UInput v-model="externalRef" maxlength="255" class="w-full" />
        </UFormField>

        <div class="flex justify-end gap-2">
          <UButton
            color="neutral"
            variant="ghost"
            :label="t('common.cancel')"
            @click="open = false"
          />
          <UButton type="submit" :loading="pending" :label="pending ? t('instances.create.creating') : t('instances.create.submit')" />
        </div>
      </form>

      <OneTimeKeyDisplay v-else :api-key="created.instance_api_key" />
    </template>

    <template v-if="created" #footer>
      <div class="flex justify-end">
        <UButton :label="t('common.done')" @click="open = false" />
      </div>
    </template>
  </UModal>
</template>
