import type { AccountUser, CreateUserInput } from '~/types/api'

// Typed client for the admin-only users endpoints. Every call runs in the
// session scope (the wzap_session cookie travels automatically); user
// sessions get 403 and the UI keeps these screens absent for them. Failures
// throw ApiError with the envelope code (conflict on duplicate email or on
// deleting an owner with instances, unprocessable_entity on invalid fields)
// so screens can render scoped messages.
export function useAccounts() {
  const { api, raw } = useApi()

  async function listUsers(): Promise<AccountUser[]> {
    return await api<AccountUser[]>('/users')
  }

  // POST /users answers 201 with the created user. An omitted instance_quota
  // applies the server default; 0 means unlimited.
  async function createUser(input: CreateUserInput): Promise<AccountUser> {
    const body: Record<string, unknown> = {
      email: input.email,
      password: input.password,
      role: input.role
    }
    if (input.instance_quota !== undefined) {
      body.instance_quota = input.instance_quota
    }
    return await api<AccountUser>('/users', { method: 'POST', body })
  }

  // DELETE answers 204 with no envelope, so it goes through the raw client.
  // Deleting an owner that still owns instances answers 409 and removes
  // nothing; there is no transfer and no cascade.
  async function deleteUser(id: string): Promise<void> {
    await raw(`/users/${id}`, { method: 'DELETE' })
  }

  // PATCH /users/{id} edits the per-user instance quota and answers 200 with
  // the updated user. 0 means unlimited.
  async function updateUserQuota(id: string, quota: number): Promise<AccountUser> {
    return await api<AccountUser>(`/users/${id}`, {
      method: 'PATCH',
      body: { instance_quota: quota }
    })
  }

  return {
    listUsers,
    createUser,
    deleteUser,
    updateUserQuota
  }
}
