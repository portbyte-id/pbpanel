import {
  type QueryClient,
  queryOptions,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"

import {
  api,
  type AppAction,
  type AppInput,
  type CreateSiteInput,
  type FirewallPending,
  type FirewallRuleInput,
  type FirewallState,
  type IssueCertInput,
  type IssuePanelCertInput,
  type UploadCertInput,
  type NginxSettings,
  type PHPSettings,
  type PoolSettings,
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
  php: ["php"] as const,
  phpSettings: (version: string) => ["php", version, "settings"] as const,
  phpIni: (version: string) => ["php", version, "ini"] as const,
  firewall: ["firewall"] as const,
  firewallState: ["firewall", "state"] as const,
  firewallRules: ["firewall", "rules"] as const,
  firewallBlocklist: ["firewall", "blocklist"] as const,
  sitePool: (domain: string) => ["sites", domain, "pool"] as const,
  siteTLS: (domain: string) => ["sites", domain, "tls"] as const,
  tlsCerts: ["tls", "certs"] as const,
  tlsProviders: ["tls", "providers"] as const,
  panelCert: ["tls", "panel"] as const,
  apps: ["apps"] as const,
  runtimes: ["runtimes"] as const,
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
export type ConfigSource =
  | { kind: "site"; domain: string }
  | { kind: "nginx" }
  | { kind: "php-ini"; version: string }

function configFileKey(source: ConfigSource) {
  switch (source.kind) {
    case "site":
      return queryKeys.siteFile(source.domain)
    case "php-ini":
      return queryKeys.phpIni(source.version)
    default:
      return queryKeys.nginxConf
  }
}

/** The service that checks and reloads a raw file, as the operator knows it. */
export function configService(source: ConfigSource): string {
  return source.kind === "php-ini" ? `PHP ${source.version}` : "nginx"
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
    queryFn: () => {
      switch (source.kind) {
        case "site":
          return api.siteFile(source.domain)
        case "php-ini":
          return api.phpIni(source.version)
        default:
          return api.nginxConf()
      }
    },
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
    mutationFn: (content: string) => {
      switch (source.kind) {
        case "site":
          return api.saveSiteFile(source.domain, content)
        case "php-ini":
          return api.savePHPIni(source.version, content)
        default:
          return api.saveNginxConf(content)
      }
    },
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

/**
 * The PHP versions. A pull changes a version's state behind the page, so the
 * list is re-read every few seconds while one is running.
 */
export function usePHP() {
  return useQuery({
    queryKey: queryKeys.php,
    queryFn: () => api.php(),
    refetchInterval: (query) =>
      query.state.data?.versions.some((v) => v.state === "pulling") ? 3_000 : 15_000,
  })
}

export function usePullPHP() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (version: string) => api.pullPHP(version),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.php, exact: true }),
  })
}

export function usePHPSettings(version: string) {
  return useQuery({
    queryKey: queryKeys.phpSettings(version),
    queryFn: () => api.phpSettings(version),
  })
}

export function useSavePHPSettings(version: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (settings: PHPSettings) => api.savePHPSettings(version, settings),
    onSuccess: (saved) => client.setQueryData(queryKeys.phpSettings(version), saved),
  })
}

export function useSitePool(domain: string) {
  return useQuery({ queryKey: queryKeys.sitePool(domain), queryFn: () => api.sitePool(domain) })
}

export function useSaveSitePool(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (settings: PoolSettings) => api.saveSitePool(domain, settings),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.sitePool(domain) }),
  })
}

/** A switch changes the site, its pool (new socket) and both versions' site lists. */
export function useSwitchPHP(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (version: string) => api.switchPHP(domain, version),
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.sites }),
        client.invalidateQueries({ queryKey: queryKeys.php, exact: true }),
      ]),
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

export function useFirewall() {
  return useQuery({ queryKey: queryKeys.firewallState, queryFn: () => api.firewall() })
}

export function useFirewallRules() {
  return useQuery({ queryKey: queryKeys.firewallRules, queryFn: () => api.firewallRules() })
}

export function useFirewallBlocklist() {
  return useQuery({ queryKey: queryKeys.firewallBlocklist, queryFn: () => api.firewallBlocklist() })
}

/** Rule and blocklist writes change `dirty`, so they refresh the status too. */
function useFirewallInvalidate() {
  const client = useQueryClient()
  return () => client.invalidateQueries({ queryKey: queryKeys.firewall })
}

export function useSaveFirewallRule() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    mutationFn: (input: { id?: string; rule: FirewallRuleInput }) =>
      input.id === undefined
        ? api.createFirewallRule(input.rule)
        : api.updateFirewallRule(input.id, input.rule),
    onSuccess: invalidate,
  })
}

export function useDeleteFirewallRule() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    mutationFn: (id: string) => api.deleteFirewallRule(id),
    onSuccess: invalidate,
  })
}

export function useAddBlocklistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    mutationFn: (input: { addr: string; comment?: string }) => api.addBlocklistEntry(input),
    onSuccess: invalidate,
  })
}

export function useRemoveBlocklistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    mutationFn: (id: string) => api.removeBlocklistEntry(id),
    onSuccess: invalidate,
  })
}

/**
 * Enable and apply answer with the pending apply. It is written into the cached
 * state at once so the countdown starts without waiting for the refetch.
 */
function useFirewallApplyMutation(fn: () => Promise<FirewallPending>) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (pending) => {
      client.setQueryData<FirewallState>(queryKeys.firewallState, (old) =>
        old ? { ...old, enabled: true, pending } : old,
      )
      return client.invalidateQueries({ queryKey: queryKeys.firewall })
    },
  })
}

export function useEnableFirewall() {
  return useFirewallApplyMutation(() => api.enableFirewall())
}

export function useApplyFirewall() {
  return useFirewallApplyMutation(() => api.applyFirewall())
}

/** Confirm, revert and disable all end any pending apply, so everything is refetched. */
export function useConfirmFirewall() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    mutationFn: (token: string) => api.confirmFirewall(token),
    onSuccess: invalidate,
  })
}

export function useRevertFirewall() {
  const invalidate = useFirewallInvalidate()
  return useMutation({ mutationFn: () => api.revertFirewall(), onSuccess: invalidate })
}

export function useDisableFirewall() {
  const invalidate = useFirewallInvalidate()
  return useMutation({ mutationFn: () => api.disableFirewall(), onSuccess: invalidate })
}

/* --- Apps and runtimes (M5) ----------------------------------------------- */

/** The list polls: an app's state changes under it (a crash, a restart) with no request. */
export function useApps() {
  return useQuery({ queryKey: queryKeys.apps, queryFn: () => api.apps(), refetchInterval: 5_000 })
}

export function useSaveApp() {
  const client = useQueryClient()
  return useMutation({
    // `name` set means an edit: the name is the program name and cannot change.
    mutationFn: (input: { name?: string; app: AppInput }) =>
      input.name === undefined ? api.createApp(input.app) : api.updateApp(input.name, input.app),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.apps }),
  })
}

export function useDeleteApp() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => api.deleteApp(name),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.apps }),
  })
}

export function useControlApp() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; action: AppAction }) =>
      api.controlApp(input.name, input.action),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.apps }),
  })
}

/** Polls faster than the apps list: an install is a job that ends by a version appearing. */
export function useRuntimes() {
  return useQuery({
    queryKey: queryKeys.runtimes,
    queryFn: () => api.runtimes(),
    refetchInterval: 4_000,
  })
}

export function useInstallNode() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (version: string) => api.installNode(version),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.runtimes }),
  })
}

export function useUninstallNode() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (version: string) => api.uninstallNode(version),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.runtimes }),
  })
}

/* --- TLS (M6) ------------------------------------------------------------ */

/** A certificate change on one site also changes the all-certificates list. */
function invalidateSiteTLS(client: QueryClient, domain: string) {
  void client.invalidateQueries({ queryKey: queryKeys.siteTLS(domain) })
  void client.invalidateQueries({ queryKey: queryKeys.tlsCerts })
}

export function useSiteTLS(domain: string) {
  return useQuery({ queryKey: queryKeys.siteTLS(domain), queryFn: () => api.siteTLS(domain) })
}

export function useUploadSiteCert(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: UploadCertInput) => api.uploadSiteCert(domain, input),
    onSuccess: () => invalidateSiteTLS(client, domain),
  })
}

/** Resolves to the job id to follow. The certificate is refetched when the job's output closes. */
export function useIssueSiteCert(domain: string) {
  return useMutation({ mutationFn: (input: IssueCertInput) => api.issueSiteCert(domain, input) })
}

export function useRenewSiteCert(domain: string) {
  return useMutation({ mutationFn: () => api.renewSiteCert(domain) })
}

/** Called when a TLS job's output closes, whatever the job's outcome. */
export function useRefreshTLS() {
  const client = useQueryClient()
  return () => {
    void client.invalidateQueries({ queryKey: queryKeys.sites })
    void client.invalidateQueries({ queryKey: ["tls"] })
  }
}

export function useSetSiteRedirect(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (on: boolean) => api.setSiteRedirect(domain, on),
    onSuccess: () => invalidateSiteTLS(client, domain),
  })
}

export function useRemoveSiteCert(domain: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.removeSiteCert(domain),
    onSuccess: () => invalidateSiteTLS(client, domain),
  })
}

export function useTLSCerts() {
  return useQuery({ queryKey: queryKeys.tlsCerts, queryFn: () => api.tlsCerts() })
}

export function useTLSProviders() {
  return useQuery({ queryKey: queryKeys.tlsProviders, queryFn: () => api.tlsProviders() })
}

export function useSaveTLSProvider() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { name: string; kind: string; apiToken: string }) =>
      api.saveTLSProvider(input.name, input.kind, input.apiToken),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.tlsProviders }),
  })
}

export function useDeleteTLSProvider() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => api.deleteTLSProvider(name),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.tlsProviders }),
  })
}

export function usePanelCert() {
  return useQuery({ queryKey: queryKeys.panelCert, queryFn: () => api.panelCert() })
}

export function useUploadPanelCert() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { certificate: string; chain?: string; private_key: string }) =>
      api.uploadPanelCert(input),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.panelCert }),
  })
}

export function useIssuePanelCert() {
  return useMutation({ mutationFn: (input: IssuePanelCertInput) => api.issuePanelCert(input) })
}
