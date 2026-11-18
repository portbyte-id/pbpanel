import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import type { JobRecord } from "@/lib/jobs"
import { JobsPage } from "@/pages/JobsPage"

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      jobs: vi.fn(),
    },
  }
})

// The drawer streams over EventSource; the page only needs to say which job it was asked to watch.
vi.mock("@/components/JobsDrawer", () => ({
  JobsDrawer: ({ jobId }: { jobId: string | null }) =>
    jobId ? <div role="dialog">watching {jobId}</div> : null,
}))

const RUNNING_ID = "3f9a1c2b8d7e4f60a1b2c3d4e5f60718"
const FAILED_ID = "9c0d1e2f3a4b5c6d7e8f901234567890"

const job = (over: Partial<JobRecord> = {}): JobRecord => ({
  id: RUNNING_ID,
  kind: "php.build",
  state: "running",
  created_at: "2026-11-18T09:00:00.000Z",
  started_at: "2026-11-18T09:00:01.000Z",
  exit_code: 0,
  error: "",
  last_seq: 12,
  dropped: 0,
  truncated: false,
  ...over,
})

const finished: JobRecord = job({
  id: FAILED_ID,
  kind: "nginx.update",
  state: "failed",
  finished_at: "2026-11-18T09:02:00.000Z",
  error: "nginx -t rejected the new config",
  last_seq: 40,
})

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <JobsPage />
    </QueryClientProvider>,
  )
  return userEvent.setup()
}

describe("JobsPage", () => {
  beforeEach(() => {
    vi.mocked(api.jobs).mockReset()
  })

  it("lists running jobs by default and asks the API for active jobs only", async () => {
    vi.mocked(api.jobs).mockResolvedValue([job()])
    setup()

    const row = await screen.findByTestId("job-row")
    expect(within(row).getByText("php.build")).toBeInTheDocument()
    expect(within(row).getByText("Running")).toBeInTheDocument()
    expect(api.jobs).toHaveBeenCalledWith({ active: true })
    expect(screen.getByRole("button", { name: "Running" })).toHaveAttribute("aria-pressed", "true")
  })

  it("says so when nothing is running", async () => {
    vi.mocked(api.jobs).mockResolvedValue([])
    setup()

    expect(await screen.findByText("Nothing is running")).toBeInTheDocument()
  })

  it("Recent adds the finished jobs and shows why one failed", async () => {
    vi.mocked(api.jobs).mockImplementation(async (opts) => (opts?.active ? [] : [finished]))
    const user = setup()

    await screen.findByText("Nothing is running")
    await user.click(screen.getByRole("button", { name: "Recent" }))

    const row = await screen.findByTestId("job-row")
    expect(within(row).getByText("nginx.update")).toBeInTheDocument()
    expect(within(row).getByText("Failed")).toBeInTheDocument()
    expect(within(row).getByText("nginx -t rejected the new config")).toBeInTheDocument()
    expect(api.jobs).toHaveBeenLastCalledWith({ active: false })
  })

  it("Watch opens the job's output, and a finished job says Output instead", async () => {
    vi.mocked(api.jobs).mockImplementation(async (opts) => (opts?.active ? [job()] : [job(), finished]))
    const user = setup()

    await user.click(await screen.findByRole("button", { name: `Watch php.build job ${RUNNING_ID.slice(0, 8)}` }))
    expect(screen.getByRole("dialog")).toHaveTextContent(`watching ${RUNNING_ID}`)

    await user.click(screen.getByRole("button", { name: "Recent" }))
    expect(
      await screen.findByRole("button", { name: `Show output of nginx.update job ${FAILED_ID.slice(0, 8)}` }),
    ).toHaveTextContent("Output")
  })

  it("reports a failed load with the server's words and a retry", async () => {
    vi.mocked(api.jobs).mockRejectedValue(new ApiError(500, "internal_error", "the job store is locked"))
    setup()

    expect(await screen.findByText("The jobs could not be loaded.")).toBeInTheDocument()
    expect(screen.getByText("the job store is locked")).toBeInTheDocument()
    expect(screen.getByRole("button", { name: /try again/i })).toBeInTheDocument()
  })
})
