<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
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
        <UCard
          v-for="user in users"
          :key="user.id"
        >
          <div class="flex flex-wrap items-center gap-x-6 gap-y-2">
            <div class="min-w-0 flex-1">
              <p class="truncate font-medium text-highlighted">
                {{ user.email }}
              </p>
              <p class="text-sm text-muted">
                {{ t('accounts.quotaLabel') }}: {{ quotaLabel(user) }}
              </p>
            </div>
            <UBadge variant="subtle">
              {{ user.role }}
            </UBadge>
            <div class="flex gap-2">
              <UButton
                color="neutral"
                variant="soft"
                icon="i-lucide-pencil"
                :label="t('accounts.quota.edit')"
                @click="openQuota(user)"
              />
              <UButton
                color="error"
                variant="soft"
                icon="i-lucide-trash-2"
                :label="t('accounts.delete.action')"
                @click="openDelete(user)"
              />
            </div>
          </div>
        </UCard>
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
