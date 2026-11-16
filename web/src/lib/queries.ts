import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { api, type SettingsResponse, type SettingWrite } from "@/lib/api"

/** Cache keys in one place, so a mutation invalidates exactly what it changed. */
export const queryKeys = {
  me: ["me"] as const,
  settings: ["settings"] as const,
  audit: (limit?: number) => ["audit", limit ?? null] as const,
}

/**
 * Who is signed in. It is the session probe: a 401 here is what sends the app
 * to `/login`, and it also re-issues the CSRF token after a page reload.
 */
export function meQuery() {
  return queryOptions({
    queryKey: queryKeys.me,
    queryFn: () => api.me(),
    // The session can be revoked from the CLI at any moment, so a stale answer
    // is never trusted for long.
    staleTime: 30_000,
    retry: false,
  })
}

export function useMe() {
  return useQuery(meQuery())
}

export function settingsQuery() {
  return queryOptions({
    queryKey: queryKeys.settings,
    queryFn: () => api.settings(),
  })
}

export function useSettings() {
  return useQuery(settingsQuery())
}

export function auditQuery(limit?: number) {
  return queryOptions({
    queryKey: queryKeys.audit(limit),
    queryFn: () => api.audit(limit),
  })
}

export function useAudit(limit?: number) {
  return useQuery(auditQuery(limit))
}

/**
 * Saves a list of keys. The server returns the settings in effect, so the cache
 * is replaced with the server's answer rather than a guess about what stuck.
 */
export function useSaveSettings() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (writes: SettingWrite[]) => api.saveSettings(writes),
    onSuccess: (saved: SettingsResponse) => {
      client.setQueryData(queryKeys.settings, saved)
    },
  })
}

export function useChangePassword() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { currentPassword: string; newPassword: string }) =>
      api.changePassword(input.currentPassword, input.newPassword),
    // A password change revokes every session, this one included, so anything
    // cached about the old session must not survive the redirect to /login.
    onSuccess: () => {
      client.clear()
    },
  })
}

export function useLogin() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { username: string; password: string }) =>
      api.login(input.username, input.password),
    onSuccess: (res) => {
      client.setQueryData(queryKeys.me, { username: res.username, csrf_token: res.csrf_token })
    },
  })
}

export function useLogout() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.logout(),
    onSettled: () => {
      client.clear()
    },
  })
}
