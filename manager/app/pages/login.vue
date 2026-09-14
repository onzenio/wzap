<script setup lang="ts">
import { ApiError } from '~/composables/useApi'

definePageMeta({
  layout: 'auth'
})

const { t } = useI18n()
const { login } = useAuth()

const email = ref('')
const password = ref('')
const pending = ref(false)
const failure = ref<string | null>(null)

async function onSubmit() {
  if (pending.value) {
    return
  }
  pending.value = true
  failure.value = null
  try {
    await login(email.value.trim(), password.value)
    await navigateTo('/')
  } catch (error) {
    failure.value = error instanceof ApiError ? error.message : t('auth.loginFailed')
  } finally {
    pending.value = false
  }
}

useSeoMeta({
  title: 'Sign in'
})
</script>

<template>
  <form class="flex flex-col gap-4" @submit.prevent="onSubmit">
    <div>
      <h1 class="text-lg font-semibold text-highlighted">
        {{ t('auth.loginTitle') }}
      </h1>
      <p class="mt-1 text-sm text-muted">
        {{ t('auth.loginSubtitle') }}
      </p>
    </div>

    <UAlert
      v-if="failure"
      color="error"
      variant="subtle"
      :title="failure"
    />

    <UFormField :label="t('auth.email')" name="email" required>
      <UInput
        v-model="email"
        type="email"
        autocomplete="username"
        required
        class="w-full"
      />
    </UFormField>

    <UFormField :label="t('auth.password')" name="password" required>
      <UInput
        v-model="password"
        type="password"
        autocomplete="current-password"
        required
        class="w-full"
      />
    </UFormField>

    <UButton
      type="submit"
      block
      :loading="pending"
    >
      {{ pending ? t('auth.signingIn') : t('auth.submit') }}
    </UButton>
  </form>
</template>
