<script setup lang="ts">
import { ApiError } from '~/composables/useApi'
import DeleteInstanceModal from '~/components/instances/DeleteInstanceModal.vue'
import InstanceStatusBadge from '~/components/instances/InstanceStatusBadge.vue'
import OneTimeKeyDisplay from '~/components/instances/OneTimeKeyDisplay.vue'
import PairingCard from '~/components/instances/PairingCard.vue'
import type { Instance, RotatedInstanceKey } from '~/types/api'

const { t } = useI18n()
const toast = useToast()
const route = useRoute()
const { isAdmin } = useAuth()
const { getInstance, updateInstance, disconnectInstance, rotateInstanceKey, revokeInstanceKey } = useInstances()

const id = computed(() => String(route.params.id ?? ''))

const instance = ref<Instance | null>(null)
const pending = ref(true)
const notFound = ref(false)
const failure = ref<string | null>(null)

const editName = ref('')
const editExternalRef = ref('')
const saving = ref(false)
const saveFailure = ref<string | null>(null)

const deleteOpen = ref(false)
const disconnectOpen = ref(false)
const disconnecting = ref(false)
const disconnectFailure = ref<string | null>(null)

const freshKey = ref<RotatedInstanceKey | null>(null)
const keySeen = ref(false)
const generating = ref(false)
const keyFailure = ref<string | null>(null)
const revokeOpen = ref(false)
const revoking = ref(false)

useSeoMeta({
  title: 'Instance details'
})

async function load() {
  pending.value = true
  notFound.value = false
  failure.value = null
  try {
    instance.value = await getInstance(id.value)
    editName.value = instance.value.name
    editExternalRef.value = instance.value.external_ref
    keySeen.value = hasSeenInstanceKey(instance.value.id)
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      notFound.value = true
    } else {
      failure.value = error instanceof ApiError ? error.message : t('instances.detail.loadFailed')
    }
  } finally {
    pending.value = false
  }
}

async function onSave() {
  if (!instance.value || saving.value) {
    return
  }
  const trimmedName = editName.value.trim()
  if (trimmedName === '') {
    saveFailure.value = t('instances.create.nameRequired')
    return
  }
  saving.value = true
  saveFailure.value = null
  try {
    instance.value = await updateInstance(instance.value.id, {
      name: trimmedName,
      external_ref: editExternalRef.value.trim()
    })
    toast.add({ title: t('instances.detail.saved'), color: 'success' })
  } catch (error) {
    saveFailure.value = friendlySaveError(error)
  } finally {
    saving.value = false
  }
}

function friendlySaveError(error: unknown): string {
  if (error instanceof ApiError) {
    if (error.status === 409) {
      return t('instances.detail.externalRefTaken')
    }
    return error.message
  }
  return t('instances.detail.saveFailed')
}

async function onDisconnect() {
  if (!instance.value || disconnecting.value) {
    return
  }
  disconnecting.value = true
  disconnectFailure.value = null
  try {
    await disconnectInstance(instance.value.id)
    disconnectOpen.value = false
    toast.add({ title: t('instances.detail.disconnected'), color: 'success' })
    await load()
  } catch (error) {
    disconnectFailure.value = error instanceof ApiError ? error.message : t('instances.detail.disconnectFailed')
  } finally {
    disconnecting.value = false
  }
}

function onDeleted() {
  toast.add({ title: t('instances.detail.deleted'), color: 'success' })
  navigateTo('/instances')
}

// The pairing card emits after the phone scan flips the session to
// connected; reloading refreshes the badge, JID and disconnect action.
async function onPaired() {
  await load()
}

async function onGenerate() {
  if (!instance.value || generating.value) {
    return
  }
  generating.value = true
  keyFailure.value = null
  try {
    freshKey.value = await rotateInstanceKey(instance.value.id)
    markInstanceKeySeen(instance.value.id)
    keySeen.value = true
    toast.add({ title: t('instances.key.generated'), color: 'success' })
  } catch (error) {
    keyFailure.value = error instanceof ApiError ? error.message : t('instances.key.generateFailed')
  } finally {
    generating.value = false
  }
}

async function onRevoke() {
  if (!instance.value || revoking.value) {
    return
  }
  revoking.value = true
  keyFailure.value = null
  try {
    await revokeInstanceKey(instance.value.id)
    forgetInstanceKeySeen(instance.value.id)
    keySeen.value = false
    freshKey.value = null
    revokeOpen.value = false
    toast.add({ title: t('instances.key.revoked'), color: 'success' })
  } catch (error) {
    keyFailure.value = error instanceof ApiError ? error.message : t('instances.key.revokeFailed')
  } finally {
    revoking.value = false
  }
}

function formatDateTime(value: string | null): string {
  if (!value) {
    return t('common.notSet')
  }
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

await load()
</script>

<template>
  <UDashboardPanel id="instance-detail">
    <template #header>
      <UDashboardNavbar :title="instance?.name ?? t('instances.title')">
        <template #leading>
          <UDashboardSidebarCollapse />
          <UButton
            color="neutral"
            variant="ghost"
            icon="i-lucide-arrow-left"
            @click="navigateTo('/instances')"
          />
        </template>
        <template v-if="instance" #right>
          <InstanceStatusBadge :status="instance.status" />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <div v-if="pending" class="flex flex-col gap-2">
        <USkeleton class="h-32 w-full" />
        <USkeleton class="h-48 w-full" />
      </div>

      <UEmpty
        v-else-if="notFound"
        icon="i-lucide-search-x"
        :title="t('instances.detail.notFound')"
      >
        <template #actions>
          <UButton :label="t('instances.title')" @click="navigateTo('/instances')" />
        </template>
      </UEmpty>

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

      <div v-else-if="instance" class="flex max-w-3xl flex-col gap-4">
        <UCard>
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ instance.name }}
            </h2>
          </template>
          <dl class="flex flex-col gap-2 text-sm">
            <div class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('instances.fields.jid') }}
              </dt>
              <dd class="font-mono text-highlighted">
                {{ instance.whatsapp_jid || t('common.notSet') }}
              </dd>
            </div>
            <div v-if="instance.last_error" class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('instances.fields.lastError') }}
              </dt>
              <dd class="text-right text-highlighted">
                {{ instance.last_error }}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('instances.fields.createdAt') }}
              </dt>
              <dd class="text-highlighted">
                {{ formatDateTime(instance.created_at) }}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('instances.fields.updatedAt') }}
              </dt>
              <dd class="text-highlighted">
                {{ formatDateTime(instance.updated_at) }}
              </dd>
            </div>
          </dl>
          <template v-if="instance.status === 'connected'" #footer>
            <UButton
              color="warning"
              variant="soft"
              icon="i-lucide-unplug"
              :label="t('instances.detail.disconnect')"
              @click="disconnectOpen = true"
            />
          </template>
        </UCard>

        <PairingCard
          :instance-id="instance.id"
          :status="instance.status"
          :whatsapp-jid="instance.whatsapp_jid"
          @paired="onPaired"
        />

        <UCard>
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ t('instances.fields.name') }}
            </h2>
          </template>
          <form class="flex flex-col gap-4" @submit.prevent="onSave">
            <UAlert
              v-if="saveFailure"
              color="error"
              variant="subtle"
              :title="saveFailure"
            />

            <UFormField :label="t('instances.fields.name')" name="name" required>
              <UInput
                v-model="editName"
                required
                maxlength="255"
                class="w-full"
              />
            </UFormField>

            <UFormField :label="t('instances.fields.externalRef')" :hint="t('instances.fields.externalRefHint')" name="external_ref">
              <UInput v-model="editExternalRef" maxlength="255" class="w-full" />
            </UFormField>

            <div class="flex justify-end">
              <UButton type="submit" :loading="saving" :label="saving ? t('common.saving') : t('common.save')" />
            </div>
          </form>
        </UCard>

        <UCard v-if="isAdmin">
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ t('instances.key.cardTitle') }}
            </h2>
          </template>

          <div class="flex flex-col gap-4">
            <UAlert
              v-if="keyFailure"
              color="error"
              variant="subtle"
              :title="keyFailure"
            />

            <OneTimeKeyDisplay v-if="freshKey" :api-key="freshKey.instance_api_key" />

            <template v-else>
              <!-- Debt: no has-key flag exists in the API, so the banner is
                driven by the browser-side key-seen marker. A future API
                field (e.g. has_api_key) should replace this condition. -->
              <UAlert
                v-if="!keySeen"
                color="info"
                variant="subtle"
                :title="t('instances.key.keylessTitle')"
                :description="t('instances.key.keylessBody')"
              />

              <p v-else class="text-sm text-muted">
                {{ t('instances.key.rotateHint') }}
              </p>

              <div class="flex flex-wrap gap-2">
                <UButton
                  icon="i-lucide-key-round"
                  :loading="generating"
                  :label="generating ? t('instances.key.generating') : t('instances.key.generate')"
                  @click="onGenerate"
                />
                <UButton
                  v-if="keySeen"
                  color="error"
                  variant="soft"
                  :label="t('instances.key.revoke')"
                  @click="revokeOpen = true"
                />
              </div>
            </template>
          </div>
        </UCard>

        <UCard>
          <template #header>
            <h2 class="font-medium text-error">
              {{ t('instances.detail.delete') }}
            </h2>
          </template>
          <div class="flex items-center justify-between gap-4">
            <p class="text-sm text-muted">
              {{ t('instances.delete.warning') }}
            </p>
            <UButton
              color="error"
              variant="soft"
              icon="i-lucide-trash-2"
              :label="t('instances.detail.delete')"
              @click="deleteOpen = true"
            />
          </div>
        </UCard>
      </div>
    </template>
  </UDashboardPanel>

  <UModal v-model:open="disconnectOpen" :title="t('instances.detail.disconnectConfirmTitle')" :description="t('instances.detail.disconnectConfirmBody')">
    <template #body>
      <UAlert
        v-if="disconnectFailure"
        color="error"
        variant="subtle"
        :title="disconnectFailure"
      />
    </template>
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton
          color="neutral"
          variant="ghost"
          :label="t('common.cancel')"
          @click="disconnectOpen = false"
        />
        <UButton
          color="warning"
          :loading="disconnecting"
          :label="disconnecting ? t('instances.detail.disconnecting') : t('instances.detail.disconnect')"
          @click="onDisconnect"
        />
      </div>
    </template>
  </UModal>

  <UModal v-model:open="revokeOpen" :title="t('instances.key.revokeConfirmTitle')" :description="t('instances.key.revokeConfirmBody')">
    <template #footer>
      <div class="flex justify-end gap-2">
        <UButton
          color="neutral"
          variant="ghost"
          :label="t('common.cancel')"
          @click="revokeOpen = false"
        />
        <UButton
          color="error"
          :loading="revoking"
          :label="revoking ? t('instances.key.revoking') : t('instances.key.revoke')"
          @click="onRevoke"
        />
      </div>
    </template>
  </UModal>

  <DeleteInstanceModal
    v-if="instance"
    v-model:open="deleteOpen"
    :instance="instance"
    @deleted="onDeleted"
  />
</template>
