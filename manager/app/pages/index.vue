<script setup lang="ts">
const { t } = useI18n()
const { user, scope } = useAuth()

useSeoMeta({
  title: 'Overview'
})
</script>

<template>
  <UDashboardPanel id="overview">
    <template #header>
      <UDashboardNavbar :title="t('overview.title')">
        <template #leading>
          <UDashboardSidebarCollapse />
        </template>
      </UDashboardNavbar>
    </template>

    <template #body>
      <p class="text-sm text-muted">
        {{ t('overview.welcome', { email: user?.email ?? '' }) }}
      </p>

      <div class="mt-4 grid gap-4 sm:grid-cols-2">
        <UCard>
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ t('overview.sessionCard') }}
            </h2>
          </template>
          <dl class="flex flex-col gap-2 text-sm">
            <div class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('common.email') }}
              </dt>
              <dd class="truncate text-highlighted">
                {{ user?.email }}
              </dd>
            </div>
            <div class="flex justify-between gap-4">
              <dt class="text-muted">
                {{ t('common.role') }}
              </dt>
              <dd>
                <UBadge variant="subtle">
                  {{ user?.role }}
                </UBadge>
              </dd>
            </div>
          </dl>
        </UCard>

        <UCard>
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ t('overview.scopeCard') }}
            </h2>
          </template>
          <p class="text-sm text-muted">
            {{ scope === 'global' ? t('overview.scopeGlobal') : t('overview.scopeInstance') }}
          </p>
        </UCard>

        <UCard>
          <template #header>
            <h2 class="font-medium text-highlighted">
              {{ t('overview.instancesCard') }}
            </h2>
          </template>
          <p class="text-sm text-muted">
            {{ t('overview.instancesCardBody') }}
          </p>
          <template #footer>
            <UButton icon="i-lucide-smartphone" :label="t('overview.openInstances')" @click="navigateTo('/instances')" />
          </template>
        </UCard>
      </div>
    </template>
  </UDashboardPanel>
</template>
