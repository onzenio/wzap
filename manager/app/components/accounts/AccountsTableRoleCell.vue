<script setup lang="ts">
import type { AccountUser } from '~/types/api'

// Role badge for the accounts table. The role renders raw (admin | user),
// exactly as the card list it replaces; no dedicated i18n keys exist yet and
// new accounts.table.* keys arrive with the page migration (Lote D).
defineProps<{
  user: AccountUser
}>()
</script>

<template>
  <UBadge variant="subtle">
    {{ user.role }}
  </UBadge>
</template>
