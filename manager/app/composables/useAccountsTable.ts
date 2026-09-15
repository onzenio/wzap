import type { TableColumn } from '@nuxt/ui'
import type { ComputedRef, Ref } from 'vue'
import type { AccountUser } from '~/types/api'

// Table state for the accounts list: column definitions plus the sorting /
// global-filter / pagination state. The page stays a thin composition (search
// input + UTable + pagination + quota/delete modals) and consumes the loaded
// users directly as the table data; cell rendering lives in
// components/accounts/AccountsTable*.vue. UTable is a renderer only: the
// client-side engine (email filter, comparators, slicing) lives in the page
// (see .superpowers/sdd/plan/batch-B-report.md §2), so this composable holds
// columns and state only — no filter predicate, no visibility map.
export function useAccountsTable(
  users: Ref<AccountUser[]> | ComputedRef<AccountUser[]>
) {
  const { t } = useI18n()

  // The loaded users travel straight into UTable as :data (client-side
  // sorting/filtering/pagination act on them, no API call); the reference is
  // kept so the table state always matches the list in scope.
  void users

  const sorting = ref([{ id: 'email', desc: false }])
  const globalFilter = ref('')
  const pagination = ref({ pageIndex: 0, pageSize: 10 })

  // Quota display rule, mirroring quotaLabel in pages/accounts/index.vue:
  // quota 0 means unlimited, any other quota renders as its number. The quota
  // cell (AccountsTableQuotaCell.vue) applies the same rule; keep both in
  // sync. Built inside the computed headers below so labels follow runtime
  // locale switches.
  function quotaLabel(user: AccountUser): string {
    return user.instance_quota === 0 ? t('accounts.unlimited') : String(user.instance_quota)
  }

  // Headers reuse existing keys only (common.email, common.role,
  // accounts.quotaLabel); new accounts.table.* keys arrive with the page
  // migration (Lote D). The actions column is a display column: it carries no
  // accessor and never sorts; AccountsTableActionsCell only emits edit-quota /
  // remove and the page keeps openQuota / openDelete.
  const columns = computed<TableColumn<AccountUser>[]>(() => [
    {
      id: 'email',
      accessorKey: 'email',
      header: t('common.email'),
      enableSorting: true
    },
    {
      id: 'role',
      accessorKey: 'role',
      header: t('common.role'),
      enableSorting: true
    },
    {
      id: 'instance_quota',
      accessorFn: (row: AccountUser) => quotaLabel(row),
      header: t('accounts.quotaLabel'),
      enableSorting: true
    },
    {
      id: 'actions',
      header: '',
      enableSorting: false
    }
  ])

  return { columns, sorting, globalFilter, pagination }
}
