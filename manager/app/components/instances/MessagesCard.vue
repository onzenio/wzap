<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import MessageStatusBadge from '~/components/instances/MessageStatusBadge.vue'
import type { OutboundMessage } from '~/types/api'

// Message history for one instance: first page on mount, manual refresh and
// load-more through the opaque next cursor. The parent bumps refreshKey after
// a test send is accepted and settled so the new message shows up.
const props = defineProps<{
  instanceId: string
  refreshKey?: number
}>()

const { t } = useI18n()
const toast = useToast()
const { listMessages } = useMessages()

const items = ref<OutboundMessage[]>([])
const nextCursor = ref('')
const pending = ref(true)
const loadingMore = ref(false)
const refreshing = ref(false)
const failure = ref<string | null>(null)

async function loadFirst() {
  pending.value = true
  failure.value = null
  try {
    const page = await listMessages(props.instanceId)
    items.value = page.items
    nextCursor.value = page.next_cursor
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('instances.messages.loadFailed')
  } finally {
    pending.value = false
  }
}

async function onRefresh() {
  if (refreshing.value) {
    return
  }
  refreshing.value = true
  failure.value = null
  try {
    const page = await listMessages(props.instanceId)
    items.value = page.items
    nextCursor.value = page.next_cursor
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('instances.messages.loadFailed')
  } finally {
    refreshing.value = false
  }
}

async function loadMore() {
  if (loadingMore.value || nextCursor.value === '') {
    return
  }
  loadingMore.value = true
  try {
    const page = await listMessages(props.instanceId, nextCursor.value)
    items.value = [...items.value, ...page.items]
    nextCursor.value = page.next_cursor
  } catch (error) {
    if (error instanceof ApiError && error.status === 400) {
      await loadFirst()
    } else {
      toast.add({
        title: error instanceof ApiError ? error.message : t('instances.messages.loadFailed'),
        color: 'error'
      })
    }
  } finally {
    loadingMore.value = false
  }
}

function formatDateTime(value: string | null): string {
  if (!value) {
    return t('common.notSet')
  }
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

watch(() => props.instanceId, () => {
  void loadFirst()
})

watch(() => props.refreshKey, () => {
  void onRefresh()
})

await loadFirst()
</script>

<template>
  <UCard data-testid="messages-card">
    <template #header>
      <div class="flex items-center justify-between gap-2">
        <h2 class="font-medium text-highlighted">
          {{ t('instances.messages.cardTitle') }}
        </h2>
        <UButton
          color="neutral"
          variant="ghost"
          size="sm"
          icon="i-lucide-refresh-cw"
          :loading="refreshing"
          :label="t('common.refresh')"
          @click="onRefresh"
        />
      </div>
    </template>

    <div v-if="pending" class="flex flex-col gap-2">
      <USkeleton class="h-16 w-full" />
      <USkeleton class="h-16 w-full" />
    </div>

    <UAlert
      v-else-if="failure"
      color="error"
      variant="subtle"
      :title="failure"
    >
      <template #actions>
        <UButton
          color="error"
          variant="soft"
          :label="t('common.retry')"
          @click="loadFirst"
        />
      </template>
    </UAlert>

    <UEmpty
      v-else-if="items.length === 0"
      icon="i-lucide-message-square-text"
      :title="t('instances.messages.empty')"
    />

    <div v-else class="flex flex-col gap-3">
      <UCard v-for="message in items" :key="message.id" variant="subtle">
        <div class="flex flex-wrap items-center gap-x-3 gap-y-2">
          <span class="font-mono text-xs text-muted">{{ message.type }}</span>
          <span class="min-w-0 flex-1 truncate font-mono text-sm text-highlighted">{{ message.recipient }}</span>
          <MessageStatusBadge :status="message.status" />
        </div>
        <dl class="mt-2 flex flex-col gap-1 text-sm">
          <div v-if="message.whatsapp_message_id" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.whatsappId') }}
            </dt>
            <dd class="truncate font-mono text-highlighted">
              {{ message.whatsapp_message_id }}
            </dd>
          </div>
          <div v-if="message.last_error" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.lastError') }}
            </dt>
            <dd class="text-right text-highlighted">
              {{ message.last_error }}
            </dd>
          </div>
          <div class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.createdAt') }}
            </dt>
            <dd class="text-highlighted">
              {{ formatDateTime(message.created_at) }}
            </dd>
          </div>
          <div v-if="message.delivered_at" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.deliveredAt') }}
            </dt>
            <dd class="text-highlighted">
              {{ formatDateTime(message.delivered_at) }}
            </dd>
          </div>
          <div v-if="message.read_at" class="flex justify-between gap-4">
            <dt class="text-muted">
              {{ t('instances.messages.readAt') }}
            </dt>
            <dd class="text-highlighted">
              {{ formatDateTime(message.read_at) }}
            </dd>
          </div>
        </dl>
      </UCard>

      <div v-if="nextCursor !== ''" class="flex justify-center pt-2">
        <UButton
          color="neutral"
          variant="soft"
          :loading="loadingMore"
          :label="t('common.loadMore')"
          @click="loadMore"
        />
      </div>
    </div>
  </UCard>
</template>
