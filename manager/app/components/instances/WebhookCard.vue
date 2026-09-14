<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { WEBHOOK_EVENT_TYPES } from '~/types/api'
import type { Instance } from '~/types/api'

// Webhook editor for one instance. Anyone operating the instance may save:
// the PATCH carries webhook-only fields so name and external_ref stay stored.
// An explicit empty URL unsets the webhook and an explicit empty event list
// clears the subscription; both mean no deliveries, as does enabled off.
// Validation failures (422) mirror the server message verbatim and persist
// nothing server-side.
const props = defineProps<{
  instance: Instance
}>()

const emit = defineEmits<{
  updated: [instance: Instance]
}>()

const { t } = useI18n()
const toast = useToast()
const { updateInstanceWebhook } = useInstances()

const url = ref(props.instance.webhook_url ?? '')
const enabled = ref(props.instance.webhook_enabled)
const selected = ref<Record<string, boolean>>(fromEvents(props.instance.webhook_events))
const saving = ref(false)
const failure = ref<string | null>(null)

function fromEvents(events: string[]): Record<string, boolean> {
  return Object.fromEntries(WEBHOOK_EVENT_TYPES.map(type => [type, events.includes(type)]))
}

const subscribedCount = computed(() => WEBHOOK_EVENT_TYPES.filter(type => selected.value[type]).length)

function selectAll() {
  selected.value = Object.fromEntries(WEBHOOK_EVENT_TYPES.map(type => [type, true]))
}

function selectNone() {
  selected.value = Object.fromEntries(WEBHOOK_EVENT_TYPES.map(type => [type, false]))
}

async function onSave() {
  if (saving.value) {
    return
  }
  saving.value = true
  failure.value = null
  try {
    const updated = await updateInstanceWebhook(props.instance.id, {
      webhook_url: url.value.trim(),
      webhook_enabled: enabled.value,
      webhook_events: WEBHOOK_EVENT_TYPES.filter(type => selected.value[type])
    })
    emit('updated', updated)
    toast.add({ title: t('instances.webhook.saved'), color: 'success' })
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('instances.webhook.saveFailed')
  } finally {
    saving.value = false
  }
}

// A fresh instance row (after pairing reloads or navigation) replaces the
// edited values with the stored configuration.
watch(() => props.instance.id, () => {
  url.value = props.instance.webhook_url ?? ''
  enabled.value = props.instance.webhook_enabled
  selected.value = fromEvents(props.instance.webhook_events)
  failure.value = null
})
</script>

<template>
  <UCard data-testid="webhook-card">
    <template #header>
      <h2 class="font-medium text-highlighted">
        {{ t('instances.webhook.cardTitle') }}
      </h2>
    </template>

    <form class="flex flex-col gap-4" @submit.prevent="onSave">
      <UAlert
        v-if="failure"
        color="error"
        variant="subtle"
        :title="failure"
      />

      <UFormField :label="t('instances.webhook.url')" :hint="t('instances.webhook.urlHint')" name="webhook_url">
        <UInput
          v-model="url"
          type="url"
          maxlength="2048"
          placeholder="https://hooks.example.com/wzap"
          class="w-full font-mono"
        />
      </UFormField>

      <UFormField :label="t('instances.webhook.enabled')" name="webhook_enabled">
        <USwitch v-model="enabled" />
      </UFormField>

      <div class="flex flex-col gap-2">
        <div class="flex items-center justify-between gap-2">
          <span class="text-sm font-medium text-highlighted">{{ t('instances.webhook.events') }}</span>
          <div class="flex gap-1">
            <UButton
              type="button"
              color="neutral"
              variant="ghost"
              size="xs"
              :label="t('instances.webhook.selectAll')"
              @click="selectAll"
            />
            <UButton
              type="button"
              color="neutral"
              variant="ghost"
              size="xs"
              :label="t('instances.webhook.selectNone')"
              @click="selectNone"
            />
          </div>
        </div>
        <p class="text-sm text-muted">
          {{ t('instances.webhook.eventsHint') }}
        </p>
        <div class="flex flex-col gap-2">
          <UCheckbox
            v-for="type in WEBHOOK_EVENT_TYPES"
            :key="type"
            v-model="selected[type]"
            :label="type"
          />
        </div>
        <UAlert
          v-if="subscribedCount === 0"
          color="warning"
          variant="subtle"
          :title="t('instances.webhook.noEventsTitle')"
          :description="t('instances.webhook.noEventsBody')"
        />
      </div>

      <div class="flex justify-end">
        <UButton
          type="submit"
          data-testid="webhook-save"
          :loading="saving"
          :label="saving ? t('common.saving') : t('common.save')"
        />
      </div>
    </form>
  </UCard>
</template>
