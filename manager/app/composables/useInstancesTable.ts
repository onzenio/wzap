import type { TableColumn } from '@nuxt/ui'
import type { ComputedRef, Ref } from 'vue'
import type { Instance } from '~/types/api'

// Table state for the instances list: column definitions plus the TanStack
// sorting / global-filter / pagination state. The page stays a thin
// composition (search input + UTable + pagination + cursor "load more") and
// consumes the loaded items directly as the table data; cell rendering lives
// in components/instances/InstancesTable*.vue.
export function useInstancesTable(
  items: Ref<Instance[]> | ComputedRef<Instance[]>,
  ownerEmails: Ref<Record<string, string>> | ComputedRef<Record<string, string>>,
  isAdmin: Ref<boolean> | ComputedRef<boolean>
) {
  const { t } = useI18n()

  // The loaded items travel straight into UTable as :data (client-side
  // sorting/filtering/pagination act on the accumulated cursor pages); the
  // reference is kept so the table state always matches the list in scope.
  void items

  const sorting = ref([{ id: 'name', desc: false }])
  const globalFilter = ref('')
  const pagination = ref({ pageIndex: 0, pageSize: 10 })

  // Resolves the owner column value, mirroring the card list it replaces
  // (ownerLabel in pages/instances/index.vue): the account email when known,
  // the short id as best-effort fallback, Not set when ownerless.
  function ownerLabel(instance: Instance): string {
    if (!instance.owner_user_id) {
      return t('common.notSet')
    }
    return ownerEmails.value[instance.owner_user_id] ?? instance.owner_user_id.slice(0, 8)
  }

  // Global search over the already-loaded items (no API call): matches the
  // instance name or the external reference, case-insensitively.
  function instancesGlobalFilterFn(
    row: { original: Instance },
    _columnId: string,
    filterValue: unknown
  ): boolean {
    const query = String(filterValue ?? '').trim().toLowerCase()
    if (query === '') {
      return true
    }
    return [row.original.name, row.original.external_ref].some(value =>
      (value ?? '').toLowerCase().includes(query)
    )
  }

  const ownerColumn: TableColumn<Instance> = {
    id: 'owner',
    accessorFn: (row: Instance) => ownerLabel(row),
    header: t('instances.columns.owner'),
    enableSorting: false,
    enableHiding: true
  }
  // pnpm keeps @tanstack/* transitive-only (unresolvable from app code), so
  // ColumnMeta cannot be augmented here; the admin-only marker travels as
  // untyped meta for the page to read when wiring column visibility.
  ownerColumn.meta = { ifAdmin: true } as unknown as TableColumn<Instance>['meta']

  const columns = computed<TableColumn<Instance>[]>(() => [
    {
      id: 'name',
      accessorKey: 'name',
      header: t('instances.columns.name'),
      enableSorting: true
    },
    {
      id: 'status',
      accessorKey: 'status',
      header: t('instances.columns.status'),
      enableSorting: true
    },
    {
      id: 'external_ref',
      accessorKey: 'external_ref',
      header: t('instances.columns.externalRef'),
      enableSorting: false
    },
    ownerColumn,
    {
      id: 'whatsapp_jid',
      accessorKey: 'whatsapp_jid',
      header: t('instances.columns.jid'),
      enableSorting: false,
      enableHiding: true
    }
  ])

  // The owner column renders for admins only; the page narrows this further
  // on small viewports (owner/whatsapp_jid hidden on mobile, as the cards do).
  const columnVisibility = computed<Record<string, boolean>>(() => ({
    owner: isAdmin.value
  }))

  const tableState = computed(() => ({
    columnVisibility: columnVisibility.value,
    globalFilterFn: instancesGlobalFilterFn
  }))

  return { columns, sorting, globalFilter, pagination, tableState }
}
