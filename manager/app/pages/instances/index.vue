<script setup lang="ts">
import type { TableRow } from '@nuxt/ui'
import { useMediaQuery } from '@vueuse/core'
import { ApiError } from '~/composables/useApi'
import { useInstancesTable } from '~/composables/useInstancesTable'
import CreateInstanceModal from '~/components/instances/CreateInstanceModal.vue'
import InstancesTableJidCell from '~/components/instances/InstancesTableJidCell.vue'
import InstancesTableNameCell from '~/components/instances/InstancesTableNameCell.vue'
import InstancesTableOwnerCell from '~/components/instances/InstancesTableOwnerCell.vue'
import InstancesTableStatusCell from '~/components/instances/InstancesTableStatusCell.vue'
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

const { columns, sorting, globalFilter, pagination, tableState } = useInstancesTable(items, ownerEmails, isAdmin)

// Client-side viewport mirrors the old cards (owner hidden below md, JID
// below lg). useMediaQuery is mobile-first on SSR (false until mount), so the
// first paint already hides both columns on small screens.
const isMdViewport = useMediaQuery('(min-width: 768px)')
const isLgViewport = useMediaQuery('(min-width: 1024px)')

// The admin-only marker travels as untyped column meta (see
// useInstancesTable); read it with a cast, never by importing @tanstack/*.
function isAdminOnly(columnId: string): boolean {
  const column = columns.value.find(entry => entry.id === columnId)
  return (column?.meta as unknown as { ifAdmin?: boolean } | undefined)?.ifAdmin ?? false
}

const columnVisibility = computed<Record<string, boolean>>(() => ({
  owner: (!isAdminOnly('owner') || isAdmin.value) && isMdViewport.value,
  whatsapp_jid: isLgViewport.value
}))

// Client-side engine over the accumulated cursor pages (no new API calls):
// the shared global-filter predicate from the composable selects rows, the
// page orders name/status, then slices the current page. UTable renders the
// slice as-is (no v-model:sorting/global-filter/pagination) so a single
// engine owns ordering — TanStack's row models stay out of the loop because
// UTable v4 ships no getPaginationRowModel and @tanstack/* is not importable
// (pnpm strict, no new dependency by design).
const filteredItems = computed(() => {
  const filterFn = tableState.value.globalFilterFn
  return items.value.filter(item => filterFn({ original: item }, 'name', globalFilter.value))
})

const sortedFilteredItems = computed(() => {
  const current = sorting.value[0]
  if (!current || (current.id !== 'name' && current.id !== 'status')) {
    return [...filteredItems.value]
  }
  const direction = current.desc ? -1 : 1
  return [...filteredItems.value].sort((a, b) => {
    const left = current.id === 'status' ? a.status : a.name
    const right = current.id === 'status' ? b.status : b.name
    return left.localeCompare(right) * direction
  })
})

const pageCount = computed(() => Math.max(1, Math.ceil(sortedFilteredItems.value.length / pagination.value.pageSize)))

const pagedItems = computed(() => {
  const start = pagination.value.pageIndex * pagination.value.pageSize
  return sortedFilteredItems.value.slice(start, start + pagination.value.pageSize)
})

watch(globalFilter, () => {
  pagination.value.pageIndex = 0
})

watch(sorting, () => {
  pagination.value.pageIndex = 0
})

watch(pageCount, (count) => {
  if (pagination.value.pageIndex > count - 1) {
    pagination.value.pageIndex = count - 1
  }
})

function toggleSort(columnId: string) {
  const current = sorting.value[0]
  sorting.value = [{ id: columnId, desc: current?.id === columnId ? !current.desc : false }]
}

function sortIcon(columnId: string): string {
  const current = sorting.value[0]
  if (current?.id !== columnId) {
    return 'i-lucide-arrow-up-down'
  }
  return current.desc ? 'i-lucide-arrow-down-wide-narrow' : 'i-lucide-arrow-up-narrow-wide'
}

// Table copy lives in instances.table.* (en.json); no UI literal stays here.
const loadedLabel = computed(() => t('instances.table.loadedCount', { count: items.value.length }))
const pageLabel = computed(() => t('instances.table.pageOf', { page: pagination.value.pageIndex + 1, pages: pageCount.value }))

function sortActionLabel(columnId: string): string {
  const current = sorting.value[0]
  const nextDesc = current?.id === columnId && !current.desc
  const column = columnId === 'status' ? t('instances.columns.status') : t('instances.columns.name')
  return nextDesc ? t('instances.table.sortDesc', { column }) : t('instances.table.sortAsc', { column })
}

function onSelectRow(_event: Event, row: TableRow<Instance>) {
  navigateTo(`/instances/${row.original.id}`)
}

useSeoMeta({
  title: 'Instances'
})

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
        <UInput
          v-model="globalFilter"
          icon="i-lucide-search"
          :placeholder="t('instances.table.search')"
        />

        <p class="text-sm text-muted">
          {{ loadedLabel }}
        </p>

        <UTable
          :data="pagedItems"
          :columns="columns"
          :column-visibility="columnVisibility"
          :empty="t('instances.table.noResults')"
          :ui="{ tr: 'cursor-pointer' }"
          @select="onSelectRow"
        >
          <template #name-header>
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              class="-mx-2.5"
              :label="t('instances.columns.name')"
              :icon="sortIcon('name')"
              :aria-label="sortActionLabel('name')"
              @click="toggleSort('name')"
            />
          </template>

          <template #status-header>
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              class="-mx-2.5"
              :label="t('instances.columns.status')"
              :icon="sortIcon('status')"
              :aria-label="sortActionLabel('status')"
              @click="toggleSort('status')"
            />
          </template>

          <template #name-cell="{ row }">
            <InstancesTableNameCell :instance="row.original" />
          </template>

          <template #status-cell="{ row }">
            <InstancesTableStatusCell :status="row.original.status" />
          </template>

          <template #owner-cell="{ row }">
            <InstancesTableOwnerCell :instance="row.original" :email="ownerEmails[row.original.owner_user_id ?? '']" />
          </template>

          <template #whatsapp_jid-cell="{ row }">
            <InstancesTableJidCell :instance="row.original" />
          </template>
        </UTable>

        <div class="flex items-center justify-between gap-3">
          <p class="text-sm text-muted">
            {{ pageLabel }}
          </p>
          <UPagination
            v-if="pageCount > 1"
            :page="pagination.pageIndex + 1"
            :items-per-page="pagination.pageSize"
            :total="sortedFilteredItems.length"
            @update:page="pagination.pageIndex = $event - 1"
          />
        </div>

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
