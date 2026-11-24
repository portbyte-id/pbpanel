import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useEffect, useRef, useState } from "react"

import { api } from "@/lib/api"
import {
  type TrafficRange,
  type TrafficRequest,
  type TrafficRequestQuery,
  type WafConfig,
  type WafEventQuery,
} from "@/lib/waf"

export const wafKeys = {
  all: ["waf"] as const,
  overview: ["waf", "overview"] as const,
  site: (domain: string) => ["waf", "site", domain] as const,
  traffic: (site: string, range: TrafficRange) => ["waf", "traffic", site, range] as const,
  requests: (filters: string) => ["waf", "requests", filters] as const,
  events: (filters: string) => ["waf", "events", filters] as const,
  summary: (site: string, range: TrafficRange) => ["waf", "summary", site, range] as const,
}

/** How often the monitor asks again, and how many feed rows it keeps. */
export const TRAFFIC_POLL_MS = 2_000
export const FEED_MAX_ROWS = 500

/**
 * Poll while the tab is visible. Returning `false` stops the timer, and the
 * query resumes (with a refetch) when the tab is shown again.
 */
export function pollWhileVisible(ms: number): () => number | false {
  return () => (typeof document !== "undefined" && document.hidden ? false : ms)
}

export function useWafOverview() {
  return useQuery({ queryKey: wafKeys.overview, queryFn: () => api.wafOverview(), retry: false })
}

export function useSiteWaf(domain: string) {
  return useQuery({ queryKey: wafKeys.site(domain), queryFn: () => api.siteWaf(domain), retry: false })
}

export function useSaveSiteWaf(domain: string) {
  const client = useQueryClient()
  return useMutation({
    meta: { success: "Firewall settings saved" },
    mutationFn: (config: WafConfig) => api.saveSiteWaf(domain, config),
    onSuccess: (res) => {
      client.setQueryData(wafKeys.site(domain), res)
      return client.invalidateQueries({ queryKey: wafKeys.overview })
    },
  })
}

export function useWafTraffic(site: string, range: TrafficRange) {
  return useQuery({
    queryKey: wafKeys.traffic(site, range),
    queryFn: () => api.wafTraffic(site, range),
    refetchInterval: pollWhileVisible(TRAFFIC_POLL_MS),
    retry: false,
  })
}

export function useWafEventSummary(site: string, range: TrafficRange) {
  return useQuery({
    queryKey: wafKeys.summary(site, range),
    queryFn: () => api.wafEventSummary(site, range),
    refetchInterval: pollWhileVisible(15_000),
    retry: false,
  })
}

/**
 * One page of audit events, newest first. `before` is the sequence number the page
 * starts below; undefined asks for the newest page. The previous page stays on
 * screen while the next one loads, so paging does not flash empty.
 */
export function useWafEvents(
  filters: Omit<WafEventQuery, "before" | "limit">,
  before: number | undefined,
  limit: number,
) {
  return useQuery({
    queryKey: wafKeys.events(JSON.stringify({ filters, before: before ?? null, limit })),
    queryFn: () => api.wafEvents({ ...filters, limit, before }),
    placeholderData: keepPreviousData,
    retry: false,
  })
}

/**
 * The live request feed: each poll asks only for rows after the newest one seen
 * and prepends them, so the list grows in place and stays under FEED_MAX_ROWS.
 * Pausing stops the polling; resuming asks for what was missed.
 */
export function useRequestFeed(filters: Omit<TrafficRequestQuery, "after" | "limit">, paused: boolean) {
  const key = JSON.stringify(filters)
  const [rows, setRows] = useState<TrafficRequest[]>([])
  const latest = useRef(0)

  // Declared before the query so a filter change clears the cursor before the
  // new query runs its first fetch.
  useEffect(() => {
    latest.current = 0
    setRows([])
  }, [key])

  const query = useQuery({
    queryKey: wafKeys.requests(key),
    queryFn: () =>
      api.wafRequests({ ...filters, after: latest.current > 0 ? latest.current : undefined, limit: 200 }),
    refetchInterval: paused ? false : pollWhileVisible(TRAFFIC_POLL_MS),
    gcTime: 0,
    retry: false,
  })

  const { data, dataUpdatedAt } = query
  useEffect(() => {
    if (!data) return
    latest.current = Math.max(latest.current, data.latest_seq)
    if (data.requests.length === 0) return
    setRows((prev) => {
      const seen = new Set(prev.map((r) => r.seq))
      const fresh = data.requests.filter((r) => !seen.has(r.seq))
      return fresh.length === 0 ? prev : [...fresh, ...prev].slice(0, FEED_MAX_ROWS)
    })
    // Keyed on dataUpdatedAt so each poll result is merged exactly once.
  }, [dataUpdatedAt, data])

  return { rows, query }
}
