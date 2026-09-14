<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import CreateInstanceModal from '~/components/instances/CreateInstanceModal.vue'
import InstanceStatusBadge from '~/components/instances/InstanceStatusBadge.vue'
import type { CreatedInstance, Instance } from '~/types/api'

const { t } = useI18n()
const toast = useToast()
const { isAdmin } = useAuth()
const { listInstances, listAccounts } = useInstances()

const items = ref<Instance[]>([])
const nextCursor = ref('')
const pending = ref(true)
const loadingMore = ref(false)
const failure = ref<string | null>(null)
const createOpen = ref(false)
const ownerEmails = ref<Record<string, string>>({})

useSeoMeta({
  title: 'Instances'
})

function ownerLabel(instance: Instance): string {
  if (!instance.owner_user_id) {
    return t('common.notSet')
  }
  return ownerEmails.value[instance.owner_user_id] ?? instance.owner_user_id.slice(0, 8)
}

async function loadOwners() {
  if (!isAdmin.value) {
    return
  }
  try {
    const accounts = await listAccounts()
    ownerEmails.value = Object.fromEntries(accounts.map(account => [account.id, account.email]))
  } catch {
    // Owner resolution is best-effort: the list still renders with short ids.
  }
}

async function loadFirst() {
  pending.value = true
  failure.value = null
  try {
    const page = await listInstances()
    items.value = page.items
    nextCursor.value = page.next_cursor
    await loadOwners()
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('instances.loadFailed')
  } finally {
    pending.value = false
  }
}

async function loadMore() {
  if (loadingMore.value || nextCursor.value === '') {
    return
  }
  loadingMore.value = true
  try {
    const page = await listInstances(nextCursor.value)
    items.value = [...items.value, ...page.items]
    nextCursor.value = page.next_cursor
  } catch (error) {
    toast.add({
      title: error instanceof ApiError ? error.message : t('instances.loadFailed'),
      color: 'error'
    })
  } finally {
    loadingMore.value = false
  }
}

function onCreated(instance: CreatedInstance) {
  // The one-time key lives in the modal only: strip it before the created
  // instance joins the list so it is never retained in list memory.
  const { instance_api_key: _omit, ...rest } = instance
  items.value = [rest, ...items.value]
  toast.add({ title: t('instances.create.createdToast'), color: 'success' })
}

await loadFirst()
</script>

<template>
  <UDashboardPanel id="instances">
    <template #header>
      <UDashboardNavbar :title="t('instances.title')">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton icon="i-lucide-plus" :label="t('instances.create.title')" @click="createOpen = true" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="mb-4 text-sm text-muted">
        {{ isAdmin ? t('instances.subtitleAdmin') : t('instances.subtitleUser') }}
      </p>

      <div v-if="pending" class="flex flex-col gap-2">
        <USkeleton class="h-12 w-full" />
        <USkeleton class="h-12 w-full" />
        <USkeleton class="h-12 w-full" />
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
        icon="i-lucide-smartphone"
        :title="t('instances.empty')"
      >
        <template #actions>
          <UButton icon="i-lucide-plus" :label="t('instances.create.title')" @click="createOpen = true" />
        </template>
      </UEmpty>

      <div v-else class="flex flex-col gap-3">
        <UCard
          v-for="instance in items"
          :key="instance.id"
          class="cursor-pointer transition hover:border-primary"
          @click="navigateTo(`/instances/${instance.id}`)"
        >
          <div class="flex flex-wrap items-center gap-x-6 gap-y-2">
            <div class="min-w-0 flex-1">
              <p class="truncate font-medium text-highlighted">
                {{ instance.name }}
              </p>
              <p v-if="instance.external_ref" class="truncate text-sm text-muted">
                {{ instance.external_ref }}
              </p>
            </div>
            <InstanceStatusBadge :status="instance.status" />
            <dl v-if="isAdmin" class="hidden text-sm md:block">
              <dt class="text-muted">
                {{ t('instances.columns.owner') }}
              </dt>
              <dd class="max-w-48 truncate text-highlighted">
                {{ ownerLabel(instance) }}
              </dd>
            </dl>
            <p v-if="instance.whatsapp_jid" class="hidden font-mono text-sm text-muted lg:block">
              {{ instance.whatsapp_jid }}
            </p>
          </div>
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
    </template>
  </UDashboardPanel>

  <CreateInstanceModal v-model:open="createOpen" @created="onCreated" />
</template>
