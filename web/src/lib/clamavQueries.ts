import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"

import { api } from "@/lib/api"
import { pollWhileVisible } from "@/lib/wafQueries"

export const clamavKey = ["clamav"] as const

/**
 * ClamAV's state. It answers 503 on a panel build without it, which the caller
 * shows as a fact rather than retrying. Polled while the tab is visible, because
 * an install's signature download and a daemon's start finish while the page is open.
 */
export function useClamav() {
  return useQuery({
    queryKey: clamavKey,
    queryFn: () => api.clamav(),
    retry: false,
    refetchInterval: pollWhileVisible(10_000),
  })
}

/** The install, as a job. */
export function useInstallClamav() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "ClamAV install started" },
    mutationFn: () => api.installClamav(),
    onSuccess: () => client.invalidateQueries({ queryKey: clamavKey }),
  })
}

/** The removal, as a job. */
export function useUninstallClamav() {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "ClamAV uninstall started" },
    mutationFn: () => api.uninstallClamav(),
    onSuccess: () => client.invalidateQueries({ queryKey: clamavKey }),
  })
}
