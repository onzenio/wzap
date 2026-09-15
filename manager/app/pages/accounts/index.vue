<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import { useAccountsTable } from '~/composables/useAccountsTable'
import AccountsTableActionsCell from '~/components/accounts/AccountsTableActionsCell.vue'
import AccountsTableQuotaCell from '~/components/accounts/AccountsTableQuotaCell.vue'
import AccountsTableRoleCell from '~/components/accounts/AccountsTableRoleCell.vue'
import type { AccountRole, AccountUser } from '~/types/api'

// Account management is admin-only: the sidebar hides this screen for user
// accounts and the guard below turns a direct visit back to the overview.
// Quotas are per user (0 means unlimited); deleting an owner that still owns
// instances answers 409 and removes nothing.
const { t } = useI18n()
const toast = useToast()
const { isAdmin } = useAuth()
const { listUsers, createUser, deleteUser, updateUserQuota } = useAccounts()

if (!isAdmin.value) {
  await navigateTo('/')
}

const users = ref<AccountUser[]>([])
const pending = ref(true)
const failure = ref<string | null>(null)

const { columns, sorting, globalFilter, pagination } = useAccountsTable(users)

// Client-side engine over the loaded users (no new API calls): email filter,
// email/role string ordering plus numeric quota ordering, then the current
// page slice. UTable renders the slice as-is so a single engine owns ordering
// (same renderer-only pattern as pages/instances/index.vue — UTable v4 ships
// no getPaginationRowModel and @tanstack/* is not importable).
const filteredUsers = computed(() => {
  const needle = globalFilter.value.trim().toLowerCase()
  if (needle === '') {
    return [...users.value]
  }
  return users.value.filter(user => user.email.toLowerCase().includes(needle))
})

const sortedFilteredUsers = computed(() => {
  const current = sorting.value[0]
  if (!current || (current.id !== 'email' && current.id !== 'role' && current.id !== 'instance_quota')) {
    return [...filteredUsers.value]
  }
  const direction = current.desc ? -1 : 1
  return [...filteredUsers.value].sort((a, b) => {
    // Quota sorts numerically over instance_quota (0 = Unlimited sorts as 0),
    // never over the display string returned by the column accessorFn.
    if (current.id === 'instance_quota') {
      return (a.instance_quota - b.instance_quota) * direction
    }
    const left = current.id === 'role' ? a.role : a.email
    const right = current.id === 'role' ? b.role : b.email
    return left.localeCompare(right, 'en') * direction
  })
})

const pageCount = computed(() => Math.max(1, Math.ceil(sortedFilteredUsers.value.length / pagination.value.pageSize)))

const pagedUsers = computed(() => {
  const start = pagination.value.pageIndex * pagination.value.pageSize
  return sortedFilteredUsers.value.slice(start, start + pagination.value.pageSize)
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

// Table copy lives in accounts.table.* (en.json); no UI literal stays here.
const countLabel = computed(() => t('accounts.table.loadedCount', { count: users.value.length }))
const pageLabel = computed(() => t('accounts.table.pageOf', { page: pagination.value.pageIndex + 1, pages: pageCount.value }))

function sortActionLabel(columnId: string): string {
  const current = sorting.value[0]
  const nextDesc = current?.id === columnId && !current.desc
  const column = columnId === 'role'
    ? t('common.role')
    : columnId === 'instance_quota' ? t('accounts.quotaLabel') : t('common.email')
  return nextDesc ? t('accounts.table.sortDesc', { column }) : t('accounts.table.sortAsc', { column })
}

const createOpen = ref(false)
const createEmail = ref('')
const createPassword = ref('')
const createRole = ref<AccountRole>('user')
const createQuota = ref('')
const creating = ref(false)
const createFailure = ref<string | null>(null)

const quotaTarget = ref<AccountUser | null>(null)
const quotaOpen = ref(false)
const quotaValue = ref('')
const quotaSaving = ref(false)
const quotaFailure = ref<string | null>(null)

const deleteTarget = ref<AccountUser | null>(null)
const deleteOpen = ref(false)
const deleting = ref(false)
const deleteFailure = ref<string | null>(null)

useSeoMeta({
  title: 'Accounts'
})

// Canonical quota display rule (quota 0 means unlimited), mirrored by
// useAccountsTable and AccountsTableQuotaCell — keep all three in sync.
// eslint-disable-next-line @typescript-eslint/no-unused-vars
function quotaLabel(user: AccountUser): string {
  return user.instance_quota === 0 ? t('accounts.unlimited') : String(user.instance_quota)
}

async function load() {
  pending.value = true
  failure.value = null
  try {
    users.value = await listUsers()
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('accounts.loadFailed')
  } finally {
    pending.value = false
  }
}

function resetCreate() {
  createEmail.value = ''
  createPassword.value = ''
  createRole.value = 'user'
  createQuota.value = ''
  creating.value = false
  createFailure.value = null
}

watch(createOpen, (value) => {
  if (value) {
    resetCreate()
  }
})

// An empty quota stays omitted so the server default applies; 0 is a valid
// explicit value meaning unlimited.
function parseQuota(raw: string): number | undefined | null {
  const trimmed = raw.trim()
  if (trimmed === '') {
    return undefined
  }
  const parsed = Number(trimmed)
  if (!Number.isInteger(parsed) || parsed < 0) {
    return null
  }
  return parsed
}

async function onCreate() {
  if (creating.value) {
    return
  }
  const email = createEmail.value.trim()
  if (email === '') {
    createFailure.value = t('accounts.create.emailRequired')
    return
  }
  if (createPassword.value === '') {
    createFailure.value = t('accounts.create.passwordRequired')
    return
  }
  const quota = parseQuota(createQuota.value)
  if (quota === null) {
    createFailure.value = t('accounts.create.quotaInvalid')
    return
  }
  creating.value = true
  createFailure.value = null
  try {
    const created = await createUser({ email, password: createPassword.value, role: createRole.value, instance_quota: quota })
    users.value = [created, ...users.value]
    createOpen.value = false
    toast.add({ title: t('accounts.create.createdToast'), color: 'success' })
  } catch (error) {
    createFailure.value = error instanceof ApiError ? friendlyCreateError(error) : t('accounts.create.failed')
  } finally {
    creating.value = false
  }
}

function friendlyCreateError(error: ApiError): string {
  if (error.status === 409) {
    return t('accounts.create.emailTaken')
  }
  return error.message
}

function openQuota(user: AccountUser) {
  quotaTarget.value = user
  quotaValue.value = String(user.instance_quota)
  quotaSaving.value = false
  quotaFailure.value = null
  quotaOpen.value = true
}

async function onSaveQuota() {
  if (!quotaTarget.value || quotaSaving.value) {
    return
  }
  const quota = parseQuota(quotaValue.value)
  if (quota === null || quota === undefined) {
    quotaFailure.value = t('accounts.quota.quotaInvalid')
    return
  }
  quotaSaving.value = true
  quotaFailure.value = null
  try {
    const updated = await updateUserQuota(quotaTarget.value.id, quota)
    users.value = users.value.map(user => user.id === updated.id ? updated : user)
    quotaOpen.value = false
    quotaTarget.value = null
    toast.add({ title: t('accounts.quota.updated'), color: 'success' })
  } catch (error) {
    quotaFailure.value = error instanceof ApiError ? error.message : t('accounts.quota.saveFailed')
  } finally {
    quotaSaving.value = false
  }
}

function openDelete(user: AccountUser) {
  deleteTarget.value = user
  deleting.value = false
  deleteFailure.value = null
  deleteOpen.value = true
}

async function onDelete() {
  if (!deleteTarget.value || deleting.value) {
    return
  }
  deleting.value = true
  deleteFailure.value = null
  try {
    await deleteUser(deleteTarget.value.id)
    users.value = users.value.filter(user => user.id !== deleteTarget.value?.id)
    deleteOpen.value = false
    deleteTarget.value = null
    toast.add({ title: t('accounts.delete.deleted'), color: 'success' })
  } catch (error) {
    deleteFailure.value = error instanceof ApiError ? friendlyDeleteError(error) : t('accounts.delete.failed')
  } finally {
    deleting.value = false
  }
}

function friendlyDeleteError(error: ApiError): string {
  if (error.status === 409) {
    return t('accounts.delete.ownsInstances')
  }
  return error.message
}

if (isAdmin.value) {
  await load()
}
</script>

<template>
  <UDashboardPanel id="accounts">
    <template #header>
      <UDashboardNavbar :title="t('accounts.title')">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
        <template #right>
          <UButton icon="i-lucide-plus" :label="t('accounts.create.title')" @click="createOpen = true" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="mb-4 text-sm text-muted">
        {{ t('accounts.subtitle') }}
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
            @click="load"
          />
        </template>
      </UAlert>

      <UEmpty
        v-else-if="users.length === 0"
        icon="i-lucide-users"
        :title="t('accounts.empty')"
      >
        <template #actions>
          <UButton icon="i-lucide-plus" :label="t('accounts.create.title')" @click="createOpen = true" />
        </template>
      </UEmpty>

      <div v-else class="flex flex-col gap-3">
        <UInput
          v-model="globalFilter"
          icon="i-lucide-search"
          :placeholder="t('accounts.table.search')"
        />

        <p class="text-sm text-muted">
          {{ countLabel }}
        </p>

        <UTable
          :data="pagedUsers"
          :columns="columns"
          :empty="t('accounts.table.noResults')"
        >
          <template #email-header>
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              class="-mx-2.5"
              :label="t('common.email')"
              :icon="sortIcon('email')"
              :aria-label="sortActionLabel('email')"
              @click="toggleSort('email')"
            />
          </template>

          <template #role-header>
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              class="-mx-2.5"
              :label="t('common.role')"
              :icon="sortIcon('role')"
              :aria-label="sortActionLabel('role')"
              @click="toggleSort('role')"
            />
          </template>

          <template #instance_quota-header>
            <UButton
              color="neutral"
              variant="ghost"
              size="xs"
              class="-mx-2.5"
              :label="t('accounts.quotaLabel')"
              :icon="sortIcon('instance_quota')"
              :aria-label="sortActionLabel('instance_quota')"
              @click="toggleSort('instance_quota')"
            />
          </template>

          <template #role-cell="{ row }">
            <AccountsTableRoleCell :user="row.original" />
          </template>

          <template #instance_quota-cell="{ row }">
            <AccountsTableQuotaCell :user="row.original" />
          </template>

          <template #actions-cell="{ row }">
            <AccountsTableActionsCell
              :user="row.original"
              @edit-quota="openQuota($event)"
              @remove="openDelete($event)"
            />
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
            :total="sortedFilteredUsers.length"
            @update:page="pagination.pageIndex = $event - 1"
          />
        </div>
      </div>
    </template>
  </UDashboardPanel>

  <UModal v-model:open="createOpen" :title="t('accounts.create.title')" :description="t('accounts.create.body')">
    <template #body>
      <form class="flex flex-col gap-4" @submit.prevent="onCreate">
        <UAlert
          v-if="createFailure"
          color="error"
          variant="subtle"
          :title="createFailure"
        />

        <UFormField :label="t('common.email')" name="email" required>
          <UInput
            v-model="createEmail"
            type="email"
            required
            maxlength="255"
            class="w-full"
          />
        </UFormField>

        <UFormField :label="t('auth.password')" name="password" required>
          <UInput
            v-model="createPassword"
            type="password"
            required
            class="w-full"
          />
        </UFormField>

        <UFormField :label="t('common.role')" name="role" required>
          <USelect
            v-model="createRole"
            :items="['admin', 'user']"
            class="w-full"
          />
        </UFormField>

        <UFormField :label="t('accounts.quotaLabel')" :hint="t('accounts.create.quotaHint')" name="instance_quota">
          <UInput
            v-model="createQuota"
            type="number"
            min="0"
            step="1"
            class="w-full"
          />
        </UFormField>

        <div class="flex justify-end gap-2">
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            :label="t('common.cancel')"
            @click="createOpen = false"
          />
          <UButton type="submit" :loading="creating" :label="creating ? t('accounts.create.creating') : t('accounts.create.submit')" />
        </div>
      </form>
    </template>
  </UModal>

  <UModal v-model:open="quotaOpen" :title="t('accounts.quota.title')" :description="t('accounts.quota.body', { email: quotaTarget?.email ?? '' })">
    <template #body>
      <form class="flex flex-col gap-4" @submit.prevent="onSaveQuota">
        <UAlert
          v-if="quotaFailure"
          color="error"
          variant="subtle"
          :title="quotaFailure"
        />

        <UFormField
          :label="t('accounts.quotaLabel')"
          :hint="t('accounts.quota.hint')"
          name="instance_quota"
          required
        >
          <UInput
            v-model="quotaValue"
            type="number"
            required
            min="0"
            step="1"
            class="w-full"
          />
        </UFormField>

        <div class="flex justify-end gap-2">
          <UButton
            type="button"
            color="neutral"
            variant="ghost"
            :label="t('common.cancel')"
            @click="quotaOpen = false"
          />
          <UButton type="submit" :loading="quotaSaving" :label="quotaSaving ? t('common.saving') : t('common.save')" />
        </div>
      </form>
    </template>
  </UModal>

  <UModal v-model:open="deleteOpen" :title="t('accounts.delete.title')" :description="t('accounts.delete.body', { email: deleteTarget?.email ?? '' })">
    <template #body>
      <UAlert
        v-if="deleteFailure"
        color="error"
        variant="subtle"
        :title="deleteFailure"
      />
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton
          color="neutral"
          variant="ghost"
          :label="t('common.cancel')"
          @click="deleteOpen = false"
        />
        <UButton
          color="error"
          :loading="deleting"
          :label="deleting ? t('accounts.delete.deleting') : t('accounts.delete.submit')"
          @click="onDelete"
        />
      </div>
    </template>
  </UModal>
</template>
