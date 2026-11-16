import {
  type QueryClient,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"

import {
  api,
  type CreateSiteInput,
  type NginxSettings,
  type SettingsResponse,
  type SettingWrite,
  type UpdateSiteInput,
} from "@/lib/api"

/** Cache keys in one place, so a mutation invalidates exactly what it changed. */
export const queryKeys = {
  me: ["me"] as const,
  settings: ["settings"] as const,
  audit: (limit?: number) => ["audit", limit ?? null] as const,
  sites: ["sites"] as const,
  site: (domain: string) => ["sites", domain] as const,
  siteFile: (domain: string) => ["sites", domain, "file"] as const,
  nginxSettings: ["nginx", "settings"] as const,
  nginxConf: ["nginx", "conf"] as const,
  nginxService: ["nginx", "service"] as const,
  configHistory: (file: string) => ["config-history", file] as const,
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

/**
 * A raw config file the operator edits. Site files and nginx.conf share one
 * editor, so they share one address.
 */
export type ConfigSource = { kind: "site"; domain: string } | { kind: "nginx" }

function configFileKey(source: ConfigSource) {
  return source.kind === "site" ? queryKeys.siteFile(source.domain) : queryKeys.nginxConf
}

export function useSites() {
  return useQuery({ queryKey: queryKeys.sites, queryFn: () => api.sites() })
}

export function useSite(domain: string) {
  return useQuery({ queryKey: queryKeys.site(domain), queryFn: () => api.site(domain) })
}

export function useCreateSite() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: CreateSiteInput) => api.createSite(input),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.sites }),
  })
}

export function useUpdateSite(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: UpdateSiteInput) => api.updateSite(domain, input),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.sites }),
  })
}

export function useDeleteSite() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (domain: string) => api.deleteSite(domain),
    onSuccess: (_res, domain) => {
      client.removeQueries({ queryKey: queryKeys.site(domain) })
      return client.invalidateQueries({ queryKey: queryKeys.sites, exact: true })
    },
  })
}

/** Regenerate rewrites the site file, so the file, its history and the feature states all change. */
export function useRegenerateSite(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.regenerateSite(domain),
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.site(domain) }),
        client.invalidateQueries({ queryKey: ["config-history"] }),
      ]),
  })
}

export function useConfigFile(source: ConfigSource) {
  return useQuery({
    queryKey: configFileKey(source),
    queryFn: () => (source.kind === "site" ? api.siteFile(source.domain) : api.nginxConf()),
  })
}

/**
 * Saves a raw file. A save can detach or re-attach a feature and always adds a
 * history entry, so those are re-read; on a rejection nothing changed on the
 * server and nothing is invalidated.
 */
export function useSaveConfigFile(source: ConfigSource) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (content: string) =>
      source.kind === "site"
        ? api.saveSiteFile(source.domain, content)
        : api.saveNginxConf(content),
    onSuccess: () => invalidateConfig(client, source),
  })
}

export function useConfigHistory(file: string | undefined) {
  return useQuery({
    queryKey: queryKeys.configHistory(file ?? ""),
    queryFn: () => api.configHistory(file ?? ""),
    enabled: file !== undefined,
  })
}

export function useRestoreConfigVersion(source: ConfigSource, file: string | undefined) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (version: number) => api.restoreConfigVersion(file ?? "", version),
    onSuccess: () => invalidateConfig(client, source),
  })
}

function invalidateConfig(client: QueryClient, source: ConfigSource) {
  const jobs = [
    client.invalidateQueries({ queryKey: configFileKey(source) }),
    client.invalidateQueries({ queryKey: ["config-history"] }),
  ]
  if (source.kind === "site") {
    jobs.push(client.invalidateQueries({ queryKey: queryKeys.site(source.domain), exact: true }))
  }
  return Promise.all(jobs)
}

export function useNginxSettings() {
  return useQuery({ queryKey: queryKeys.nginxSettings, queryFn: () => api.nginxSettings() })
}

export function useSaveNginxSettings() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (settings: NginxSettings) => api.saveNginxSettings(settings),
    onSuccess: (saved) => client.setQueryData(queryKeys.nginxSettings, saved),
  })
}

export function useNginxService() {
  return useQuery({
    queryKey: queryKeys.nginxService,
    queryFn: () => api.nginxService(),
    // The container can stop behind the panel's back; the status is a reading,
    // not a fact, so it is taken again while the page is open.
    refetchInterval: 10_000,
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
