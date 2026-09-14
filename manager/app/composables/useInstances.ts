import type {
  AccountUser,
  CreatedInstance,
  CreateInstanceInput,
  Instance,
  InstanceListPage,
  RotatedInstanceKey,
  UpdateInstanceInput
} from '~/types/api'

// localStorage marker recording that a plaintext key for the instance was
// already revealed in this browser (at create or rotate time). The API never
// re-exposes keys and exposes no has-key flag, so this marker only drives
// the keyless banner: when absent, the detail view warns the instance may
// predate keys (or its key was revoked or lost) and offers to generate one.
// Only the boolean travels to storage, never the key itself.
function keySeenStorageKey(id: string): string {
  return `wzap.manager.keySeen.${id}`
}

export function hasSeenInstanceKey(id: string): boolean {
  try {
    return localStorage.getItem(keySeenStorageKey(id)) === '1'
  } catch {
    return false
  }
}

export function markInstanceKeySeen(id: string): void {
  try {
    localStorage.setItem(keySeenStorageKey(id), '1')
  } catch {
    // Private browsing or denied storage must not break the flow.
  }
}

export function forgetInstanceKeySeen(id: string): void {
  try {
    localStorage.removeItem(keySeenStorageKey(id))
  } catch {
    // Private browsing or denied storage must not break the flow.
  }
}

// Typed client for the instance and instance-key endpoints. Every call runs
// in the session scope (the wzap_session cookie travels automatically);
// failures throw ApiError with the envelope code (quota_exceeded, conflict,
// forbidden) so screens can render scoped messages.
export function useInstances() {
  const { api, raw } = useApi()

  async function listInstances(cursor?: string): Promise<InstanceListPage> {
    const query = cursor ? { cursor } : {}
    return await api<InstanceListPage>('/instances', { query })
  }

  async function getInstance(id: string): Promise<Instance> {
    return await api<Instance>(`/instances/${id}`)
  }

  async function createInstance(input: CreateInstanceInput): Promise<CreatedInstance> {
    const body: Record<string, string> = { name: input.name }
    const externalRef = input.external_ref?.trim() ?? ''
    if (externalRef !== '') {
      body.external_ref = externalRef
    }
    return await api<CreatedInstance>('/instances', { method: 'POST', body })
  }

  async function updateInstance(id: string, input: UpdateInstanceInput): Promise<Instance> {
    return await api<Instance>(`/instances/${id}`, {
      method: 'PATCH',
      body: { name: input.name, external_ref: input.external_ref }
    })
  }

  // DELETE answers 204 with no envelope, so it goes through the raw client.
  async function deleteInstance(id: string): Promise<void> {
    await raw(`/instances/${id}`, { method: 'DELETE' })
  }

  // POST answers 204 with no envelope, so it goes through the raw client.
  async function disconnectInstance(id: string): Promise<void> {
    await raw(`/instances/${id}/disconnect`, { method: 'POST' })
  }

  // Rotate answers 200 with the fresh one-time plaintext key. Only the
  // global scope and admin sessions may call it; user sessions get 403 and
  // the UI keeps this action absent for them.
  async function rotateInstanceKey(id: string): Promise<RotatedInstanceKey> {
    return await api<RotatedInstanceKey>(`/instances/${id}/apikey/rotate`, { method: 'POST' })
  }

  // DELETE answers 204 with no envelope, so it goes through the raw client.
  // Revoking an already-keyless instance still succeeds.
  async function revokeInstanceKey(id: string): Promise<void> {
    await raw(`/instances/${id}/apikey`, { method: 'DELETE' })
  }

  // Admin-only account listing used to resolve the owner column. It throws
  // 403 for user sessions; callers gate it behind isAdmin.
  async function listAccounts(): Promise<AccountUser[]> {
    return await api<AccountUser[]>('/users')
  }

  return {
    listInstances,
    getInstance,
    createInstance,
    updateInstance,
    deleteInstance,
    disconnectInstance,
    rotateInstanceKey,
    revokeInstanceKey,
    listAccounts
  }
}
