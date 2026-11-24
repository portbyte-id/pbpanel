import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api } from "@/lib/api"
import { AuditPage } from "@/pages/AuditPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { audit: vi.fn() } }
})

const entry = (id: number) => ({
  id,
  at: "2026-11-24T09:00:00Z",
  actor: "admin",
  action: "PUT /api/settings",
  target: `setting ${id}`,
  result: "success" as const,
  detail: {},
})

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <AuditPage />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.mocked(api.audit).mockReset()
})

describe("AuditPage", () => {
  it("pages older by the oldest id on the page, and back to newer", async () => {
    const user = userEvent.setup()
    vi.mocked(api.audit)
      .mockResolvedValueOnce({ entries: [entry(60), entry(59)], limit: 50, next_before: 59 })
      .mockResolvedValueOnce({ entries: [entry(58)], limit: 50, next_before: 0 })
      .mockResolvedValueOnce({ entries: [entry(60), entry(59)], limit: 50, next_before: 59 })
    renderPage()

    const table = await screen.findByRole("table", { name: "Audit trail, newest first" })
    expect(within(table).getAllByRole("row")).toHaveLength(3)
    expect(screen.getByText("Page 1 · 2 entries, newest first")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Newer" })).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Older" }))
    await screen.findByText("Page 2 · 1 entry, newest first")
    expect(vi.mocked(api.audit).mock.calls[1]).toEqual([50, 59])
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled()

    await user.click(screen.getByRole("button", { name: "Newer" }))
    await screen.findByText("Page 1 · 2 entries, newest first")
    expect(vi.mocked(api.audit).mock.calls[2]).toEqual([50, undefined])
  })

  it("starts again from the newest page when the page size changes", async () => {
    const user = userEvent.setup()
    vi.mocked(api.audit)
      .mockResolvedValueOnce({ entries: [entry(60)], limit: 50, next_before: 60 })
      .mockResolvedValueOnce({ entries: [entry(59)], limit: 50, next_before: 0 })
      .mockResolvedValueOnce({ entries: [entry(60)], limit: 25, next_before: 0 })
    renderPage()

    await screen.findByText("Page 1 · 1 entry, newest first")
    await user.click(screen.getByRole("button", { name: "Older" }))
    await screen.findByText("Page 2 · 1 entry, newest first")

    await user.selectOptions(screen.getByLabelText("Entries per page"), "25")
    await screen.findByText("Page 1 · 1 entry, newest first")
    expect(vi.mocked(api.audit).mock.lastCall).toEqual([25, undefined])
  })
})
