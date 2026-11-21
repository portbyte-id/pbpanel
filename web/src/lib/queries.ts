import {
  type QueryClient,
  queryOptions,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query"
import { useEffect, useMemo, useState } from "react"

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
  type SSHAttemptsQuery,
  type UpdateSiteInput,
} from "@/lib/api"
import type { CronJobInput } from "@/lib/cron"
import { isActiveJob } from "@/lib/jobs"
import type { DockerAction } from "@/lib/docker"
import {
  HISTORY_CAP,
  mergeSnapshots,
  openSysinfoStream,
  type ServiceAction,
  type Snapshot,
  type SysinfoStreamStatus,
} from "@/lib/sysinfo"

/** Cache keys in one place, so a mutation invalidates exactly what it changed. */
export const queryKeys = {
  me: ["me"] as const,
  settings: ["settings"] as const,
  audit: (limit?: number) => ["audit", limit ?? null] as const,
  sites: ["sites"] as const,
  site: (domain: string) => ["sites", domain] as const,
  siteFile: (domain: string) => ["sites", domain, "file"] as const,
  siteIncludes: (domain: string) => ["sites", domain, "includes"] as const,
  siteInclude: (domain: string, name: string) => ["sites", domain, "includes", name] as const,
  nginxSettings: ["nginx", "settings"] as const,
  nginxConf: ["nginx", "conf"] as const,
  nginxService: ["nginx", "service"] as const,
  nginxImage: ["nginx", "image"] as const,
  configHistory: (file: string) => ["config-history", file] as const,
  php: ["php"] as const,
  phpSettings: (version: string) => ["php", version, "settings"] as const,
  phpIni: (version: string) => ["php", version, "ini"] as const,
  phpBuilds: (version: string) => ["php", version, "builds"] as const,
  phpExtensions: ["php", "extensions"] as const,
  firewall: ["firewall"] as const,
  firewallState: ["firewall", "state"] as const,
  firewallRules: ["firewall", "rules"] as const,
  firewallBlocklist: ["firewall", "blocklist"] as const,
  firewallAllowlist: ["firewall", "allowlist"] as const,
  sitePool: (domain: string) => ["sites", domain, "pool"] as const,
  siteTLS: (domain: string) => ["sites", domain, "tls"] as const,
  tlsCerts: ["tls", "certs"] as const,
  tlsProviders: ["tls", "providers"] as const,
  panelCert: ["tls", "panel"] as const,
  apps: ["apps"] as const,
  runtimes: ["runtimes"] as const,
  sysinfo: ["sysinfo"] as const,
  services: ["services"] as const,
  logSources: ["logs", "sources"] as const,
  docker: ["docker"] as const,
  dockerInfo: ["docker", "info"] as const,
  dockerContainers: ["docker", "containers"] as const,
  dockerStats: ["docker", "stats"] as const,
  dockerImages: ["docker", "images"] as const,
  dockerVolumes: ["docker", "volumes"] as const,
  dockerProcesses: (id: string) => ["docker", "containers", id, "top"] as const,
  dockerLogs: (id: string, tail: number) => ["docker", "containers", id, "logs", tail] as const,
  cron: ["cron"] as const,
  cronRuns: (id: number) => ["cron", id, "runs"] as const,
  jobs: (active: boolean) => ["jobs", active ? "active" : "all"] as const,
  cronRun: (id: number, runId: number) => ["cron", id, "runs", runId] as const,
  sshAttempts: (range: string, outcome: string, limit: number) =>
    ["ssh", "attempts", range, outcome, limit] as const,
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
    meta: { success: "Settings saved" },
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

export function useChangeUsername() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { currentPassword: string; newUsername: string }) =>
      api.changeUsername(input.currentPassword, input.newUsername),
    // A rename revokes every session too, so the cached `me` — which names the
    // old account — must not outlive the redirect to /login.
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
  | { kind: "site-include"; domain: string; name: string }
  | { kind: "nginx" }
  | { kind: "php-ini"; version: string }

function configFileKey(source: ConfigSource) {
  switch (source.kind) {
    case "site":
      return queryKeys.siteFile(source.domain)
    case "site-include":
      return queryKeys.siteInclude(source.domain, source.name)
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
    meta: { success: "Site created" },
    mutationFn: (input: CreateSiteInput) => api.createSite(input),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.sites }),
  })
}

export function useUpdateSite(domain: string) {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Site updated" },
    mutationFn: (input: UpdateSiteInput) => api.updateSite(domain, input),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.sites }),
  })
}

export function useDeleteSite() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Site deleted" },
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
        case "site-include":
          return api.siteInclude(source.domain, source.name)
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
    meta: { success: "Configuration saved" },
    mutationFn: (content: string) => {
      switch (source.kind) {
        case "site":
          return api.saveSiteFile(source.domain, content)
        case "site-include":
          return api.saveSiteInclude(source.domain, source.name, content)
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
  if (source.kind === "site-include") {
    jobs.push(client.invalidateQueries({ queryKey: queryKeys.siteIncludes(source.domain) }))
    jobs.push(client.invalidateQueries({ queryKey: queryKeys.site(source.domain), exact: true }))
  }
  return Promise.all(jobs)
}

/** The include files of a site, with their content, for the Config tab. */
export function useSiteIncludes(domain: string) {
  return useQuery({
    queryKey: queryKeys.siteIncludes(domain),
    queryFn: () => api.siteIncludes(domain),
  })
}

export function useNginxSettings() {
  return useQuery({ queryKey: queryKeys.nginxSettings, queryFn: () => api.nginxSettings() })
}

export function useSaveNginxSettings() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "nginx settings saved" },
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

/** The pull input. `extensions` omitted keeps the current build's extensions. */
export interface PullPHPInput {
  version: string
  extensions?: string[]
}

export function usePullPHP() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: PullPHPInput) => api.pullPHP(input.version, input.extensions),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.php, exact: true }),
  })
}

/** The curated catalogue never changes while the panel runs, so it is read once. */
export function usePHPExtensions() {
  return useQuery({
    queryKey: queryKeys.phpExtensions,
    queryFn: () => api.phpExtensions(),
    staleTime: Number.POSITIVE_INFINITY,
  })
}

/**
 * A version's builds, newest first. Read only while the history is open; it is
 * re-read every few seconds while a build is running.
 */
export function usePHPBuilds(version: string, enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.phpBuilds(version),
    queryFn: () => api.phpBuilds(version),
    enabled,
    refetchInterval: (query) =>
      query.state.data?.some((b) => b.state === "building") ? 3_000 : false,
  })
}

/** Rebuilds the version with these extensions. The job is watched by the page. */
export function useBuildPHP(version: string) {
  return useMutation({
    mutationFn: (extensions: string[]) => api.buildPHP(version, extensions),
  })
}

export function useActivatePHPBuild(version: string) {
  return useMutation({
    mutationFn: (buildId: string) => api.activatePHPBuild(version, buildId),
  })
}

/** Removing a version leaves its build history with nothing behind it. */
export function useRemovePHP(version: string) {
  const client = useQueryClient()
  return useMutation({
    mutationFn: () => api.removePHP(version),
    onSuccess: () =>
      Promise.all([
        client.invalidateQueries({ queryKey: queryKeys.php, exact: true }),
        client.invalidateQueries({ queryKey: queryKeys.phpBuilds(version), exact: true }),
      ]),
  })
}

/**
 * The nginx builds. Like the PHP list it is re-read while a build runs, and every
 * 10 seconds otherwise, so a build started elsewhere shows up here too.
 */
export function useNginxImage() {
  return useQuery({
    queryKey: queryKeys.nginxImage,
    queryFn: () => api.nginxImage(),
    refetchInterval: (query) => (query.state.data?.busy ? 3_000 : 10_000),
  })
}

export function useUpdateNginxImage() {
  return useMutation({ mutationFn: () => api.updateNginxImage() })
}

export function useNginxContainerAction() {
  return useMutation({
    mutationFn: (action: "start" | "stop" | "restart") => api.nginxContainerAction(action),
  })
}

export function useSetNginxVersion() {
  return useMutation({ mutationFn: (version: string) => api.setNginxVersion(version) })
}

export function useActivateNginxImageBuild() {
  return useMutation({ mutationFn: (buildId: string) => api.activateNginxImageBuild(buildId) })
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
    meta: { success: "PHP settings saved" },
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
    meta: { success: "PHP pool saved" },
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

export function useFirewallAllowlist() {
  return useQuery({ queryKey: queryKeys.firewallAllowlist, queryFn: () => api.firewallAllowlist() })
}

/** Rule and blocklist writes change `dirty`, so they refresh the status too. */
function useFirewallInvalidate() {
  const client = useQueryClient()
  return () => client.invalidateQueries({ queryKey: queryKeys.firewall })
}

export function useSaveFirewallRule() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    meta: { success: "Firewall rule saved" },
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
    meta: { success: "Firewall rule deleted" },
    mutationFn: (id: string) => api.deleteFirewallRule(id),
    onSuccess: invalidate,
  })
}

export function useAddBlocklistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    meta: { success: "Added to the blocklist" },
    mutationFn: (input: { addr: string; comment?: string }) => api.addBlocklistEntry(input),
    onSuccess: invalidate,
  })
}

export function useRemoveBlocklistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    meta: { success: "Removed from the blocklist" },
    mutationFn: (id: string) => api.removeBlocklistEntry(id),
    onSuccess: invalidate,
  })
}

export function useAddAllowlistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    meta: { success: "Added to the allow list" },
    mutationFn: (input: { addr: string; comment?: string }) => api.addAllowlistEntry(input),
    onSuccess: invalidate,
  })
}

export function useRemoveAllowlistEntry() {
  const invalidate = useFirewallInvalidate()
  return useMutation({
    meta: { success: "Removed from the allow list" },
    mutationFn: (id: string) => api.removeAllowlistEntry(id),
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
    meta: { success: "Removed from the blocklist" },
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
  return useMutation({ meta: { success: "Firewall disabled" }, mutationFn: () => api.disableFirewall(), onSuccess: invalidate })
}

/* --- Apps and runtimes (M5) ----------------------------------------------- */

/** The list polls: an app's state changes under it (a crash, a restart) with no request. */
export function useApps() {
  return useQuery({ queryKey: queryKeys.apps, queryFn: () => api.apps(), refetchInterval: 5_000 })
}

export function useSaveApp() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "App saved" },
    // `name` set means an edit: the name is the program name and cannot change.
    mutationFn: (input: { name?: string; app: AppInput }) =>
      input.name === undefined ? api.createApp(input.app) : api.updateApp(input.name, input.app),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.apps }),
  })
}

export function useDeleteApp() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "App deleted" },
    mutationFn: (name: string) => api.deleteApp(name),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.apps }),
  })
}

export function useControlApp() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "App command sent" },
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
    meta: { success: "Redirect updated" },
    mutationFn: (on: boolean) => api.setSiteRedirect(domain, on),
    onSuccess: () => invalidateSiteTLS(client, domain),
  })
}

export function useRemoveSiteCert(domain: string) {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Certificate removed" },
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
    meta: { success: "Provider saved" },
    mutationFn: (input: { name: string; kind: string; apiToken: string }) =>
      api.saveTLSProvider(input.name, input.kind, input.apiToken),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.tlsProviders }),
  })
}

export function useDeleteTLSProvider() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Provider deleted" },
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

export function usePanelIP() {
  return useQuery({
    queryKey: [...queryKeys.panelCert, "ip"],
    queryFn: () => api.panelIP(),
    staleTime: 60_000,
  })
}

export function useIssuePanelIPCert() {
  return useMutation({ mutationFn: (input: { ip: string; email?: string }) => api.issuePanelIPCert(input) })
}

/* --- Dashboard, logs and cron (M7) -------------------------------------- */

/**
 * The metrics: the history from `/api/sysinfo` plus each snapshot the stream
 * delivers. While the stream is not live the query polls at the sampling
 * interval, so a proxy that buffers server-sent events degrades to a slower
 * page, not a frozen one.
 */
export function useSysinfoLive() {
  const [streamed, setStreamed] = useState<Snapshot[]>([])
  const [status, setStatus] = useState<SysinfoStreamStatus>("connecting")

  const query = useQuery({
    queryKey: queryKeys.sysinfo,
    queryFn: () => api.sysinfo(),
    refetchInterval: (q) => (status === "live" ? false : (q.state.data?.interval_seconds ?? 5) * 1000),
  })

  useEffect(() => {
    const stream = openSysinfoStream({
      onSnapshot: (snapshot) => setStreamed((prev) => [...prev.slice(-(HISTORY_CAP - 1)), snapshot]),
      onStatus: setStatus,
    })
    return () => stream.close()
  }, [])

  const history = useMemo(
    () => mergeSnapshots(query.data?.history ?? [], streamed),
    [query.data, streamed],
  )
  const latest = history.at(-1) ?? query.data?.latest ?? null

  return {
    latest,
    history,
    status,
    intervalSeconds: query.data?.interval_seconds ?? 5,
    isPending: query.isPending,
    error: query.error,
  }
}

/** The service grid polls: a container or unit changes state with no request from the page. */
export function useServices() {
  return useQuery({
    queryKey: queryKeys.services,
    queryFn: () => api.services(),
    refetchInterval: 5_000,
  })
}

export function useControlService() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Service command sent" },
    mutationFn: (input: { name: string; action: ServiceAction }) =>
      api.controlService(input.name, input.action),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.services }),
  })
}

/**
 * Every site's file features, for the "detached" alert. The site list does not
 * carry them, so this is one request per site; they are read once a minute.
 */
export function useSiteDetails() {
  const sites = useSites()
  const details = useQueries({
    queries: (sites.data ?? []).map((s) => ({
      queryKey: queryKeys.site(s.domain),
      queryFn: () => api.site(s.domain),
      refetchInterval: 60_000,
    })),
  })
  return {
    sites,
    details: details.flatMap((d) => (d.data ? [d.data] : [])),
    failed: details.some((d) => d.isError),
  }
}

export function useLogSources() {
  return useQuery({ queryKey: queryKeys.logSources, queryFn: () => api.logSources() })
}

/* --- Docker -------------------------------------------------------------- */

/** Engine facts and counts. A stopped daemon answers 503, which retrying will not fix. */
export function useDockerInfo() {
  return useQuery({
    queryKey: queryKeys.dockerInfo,
    queryFn: () => api.dockerInfo(),
    refetchInterval: 10_000,
    retry: false,
  })
}

export function useDockerContainers() {
  return useQuery({
    queryKey: queryKeys.dockerContainers,
    queryFn: () => api.dockerContainers(),
    refetchInterval: 5_000,
    retry: false,
  })
}

/** One sample takes about a second on the server, so the monitor polls slower than the list. */
export function useDockerStats(enabled: boolean) {
  return useQuery({
    queryKey: queryKeys.dockerStats,
    queryFn: () => api.dockerStats(),
    refetchInterval: 5_000,
    enabled,
    retry: false,
  })
}

export function useDockerImages() {
  return useQuery({ queryKey: queryKeys.dockerImages, queryFn: () => api.dockerImages(), retry: false })
}

export function useDockerVolumes() {
  return useQuery({ queryKey: queryKeys.dockerVolumes, queryFn: () => api.dockerVolumes(), retry: false })
}

export function useDockerProcesses(id: string | null) {
  return useQuery({
    queryKey: queryKeys.dockerProcesses(id ?? ""),
    queryFn: () => api.dockerProcesses(id ?? ""),
    enabled: id !== null,
    refetchInterval: 5_000,
    retry: false,
  })
}

export function useDockerLogs(id: string | null, tail: number) {
  return useQuery({
    queryKey: queryKeys.dockerLogs(id ?? "", tail),
    queryFn: () => api.dockerLogs(id ?? "", tail),
    enabled: id !== null,
    refetchInterval: 5_000,
    retry: false,
  })
}

export function useDockerControl() {
  const client = useQueryClient()
  return useMutation({
    mutationFn: (input: { id: string; action: DockerAction }) => api.dockerControl(input.id, input.action),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.docker }),
  })
}

export function useDockerRemoveContainer() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Container removed" },
    mutationFn: (input: { id: string; force: boolean }) => api.dockerRemoveContainer(input.id, input.force),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.docker }),
  })
}

export function useDockerRemoveImage() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Image removed" },
    mutationFn: (id: string) => api.dockerRemoveImage(id),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.docker }),
  })
}

export function useDockerRemoveVolume() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Volume removed" },
    mutationFn: (name: string) => api.dockerRemoveVolume(name),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.docker }),
  })
}

/** The job list polls: `next_run` and `last_run` move without a request from the page. */
export function useCronJobs() {
  return useQuery({
    queryKey: queryKeys.cron,
    queryFn: () => api.cronJobs(),
    refetchInterval: 10_000,
  })
}

/**
 * The job list. It polls every 2 seconds while any listed job is live, so progress
 * shows without a reload, and every 10 seconds once the list is quiet.
 */
export function useJobs({ active = false }: { active?: boolean } = {}) {
  return useQuery({
    queryKey: queryKeys.jobs(active),
    queryFn: () => api.jobs({ active }),
    refetchInterval: (q) => (q.state.data?.some(isActiveJob) ? 2_000 : 10_000),
  })
}

export function useSaveCronJob() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Cron job saved" },
    // `id` set means an edit: the name cannot change.
    mutationFn: (input: { id?: number; job: CronJobInput }) =>
      input.id === undefined ? api.createCronJob(input.job) : api.updateCronJob(input.id, input.job),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.cron }),
  })
}

export function useDeleteCronJob() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Cron job deleted" },
    mutationFn: (id: number) => api.deleteCronJob(id),
    onSuccess: (_res, id) => {
      client.removeQueries({ queryKey: queryKeys.cronRuns(id) })
      return client.invalidateQueries({ queryKey: queryKeys.cron, exact: true })
    },
  })
}

export function useSetCronEnabled() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Cron job updated" },
    mutationFn: (input: { id: number; enabled: boolean }) => api.setCronEnabled(input.id, input.enabled),
    onSuccess: () => client.invalidateQueries({ queryKey: queryKeys.cron }),
  })
}

export function useRunCronJob() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Cron job started" },
    mutationFn: (id: number) => api.runCronJob(id),
    onSettled: () => client.invalidateQueries({ queryKey: queryKeys.cron }),
  })
}

/** A run that has not ended is re-read every few seconds so its output and status arrive. */
export function useCronRuns(id: number | null) {
  return useQuery({
    queryKey: queryKeys.cronRuns(id ?? 0),
    queryFn: () => api.cronRuns(id ?? 0, 30),
    enabled: id !== null,
    refetchInterval: (q) => (q.state.data?.some((r) => r.status === "running") ? 3_000 : 15_000),
  })
}

export function useCronRun(id: number, runId: number | null) {
  return useQuery({
    queryKey: queryKeys.cronRun(id, runId ?? 0),
    queryFn: () => api.cronRun(id, runId ?? 0),
    enabled: runId !== null,
    refetchInterval: (q) => (q.state.data?.status === "running" ? 3_000 : false),
  })
}

/* --- SSH login attempts -------------------------------------------------- */

/**
 * The same value after it has stopped changing for `ms`, so typing into a filter
 * does not ask the server once per key.
 */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

/**
 * The sshd login-attempt feed for one window. A host with no readable sshd
 * journal answers 503 `unavailable`; that is a permanent condition for the host,
 * so it is not retried — the page explains it instead.
 */
export function useSSHAttempts(query: SSHAttemptsQuery, refetchInterval: number | false = false) {
  return useQuery({
    queryKey: queryKeys.sshAttempts(query.range ?? "", query.outcome ?? "", query.limit ?? 0),
    queryFn: () => api.sshAttempts(query),
    refetchInterval,
    retry: false,
  })
}
