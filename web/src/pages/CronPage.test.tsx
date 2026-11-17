import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError, type SettingsResponse } from "@/lib/api"
import type { CronJob, CronRun } from "@/lib/cron"
import { CronPage } from "@/pages/CronPage"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children: React.ReactNode; to: string; className?: string }) => (
    <a href={String(to)} className={className}>
      {children}
    </a>
  ),
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return {
    ...actual,
    api: {
      cronJobs: vi.fn(),
      createCronJob: vi.fn(),
      updateCronJob: vi.fn(),
      deleteCronJob: vi.fn(),
      setCronEnabled: vi.fn(),
      runCronJob: vi.fn(),
      cronRuns: vi.fn(),
      cronRun: vi.fn(),
      settings: vi.fn(),
    },
  }
})

const job = (over: Partial<CronJob> = {}): CronJob => ({
  id: 1,
  name: "backup",
  schedule: "0 3 * * *",
  kind: "argv",
  user: "root",
  argv: ["/usr/bin/true"],
  timeout_seconds: 3600,
  overlap: "skip",
  retention: 50,
  enabled: true,
  created_at: "2026-11-01T00:00:00Z",
  updated_at: "2026-11-01T00:00:00Z",
  next_run: "2026-11-18T03:00:00+07:00",
  last_run: null,
  ...over,
})

const run = (over: Partial<CronRun> = {}): CronRun => ({
  id: 11,
  job_id: 1,
  source: "schedule",
  started_at: "2026-11-17T03:00:00Z",
  finished_at: "2026-11-17T03:00:09Z",
  exit_code: 0,
  status: "succeeded",
  output_tail: "all good\n",
  truncated: false,
  ...over,
})

const settingsWith = (allowShell: boolean): SettingsResponse => ({
  groups: [
    {
      name: "Cron",
      keys: [
        {
          name: "cron.allow_shell",
          type: "bool",
          value: String(allowShell),
          default: "false",
          is_default: !allowShell,
          restart_required: false,
          description: "",
          group: "Cron",
        },
      ],
    },
  ],
})

function setup(jobs: CronJob[], allowShell: boolean | null = false) {
  vi.mocked(api.cronJobs).mockResolvedValue(jobs)
  if (allowShell === null) vi.mocked(api.settings).mockRejectedValue(new ApiError(500, "internal_error", "x"))
  else vi.mocked(api.settings).mockResolvedValue(settingsWith(allowShell))
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <CronPage />
    </QueryClientProvider>,
  )
}

const row = async (name: string) => {
  await screen.findAllByTestId("cron-row")
  const el = screen.getAllByTestId("cron-row").find((r) => r.getAttribute("data-job") === name)
  if (!el) throw new Error(`no row for ${name}`)
  return within(el)
}

describe("CronPage", () => {
  beforeEach(() => {
    for (const fn of Object.values(api)) (fn as ReturnType<typeof vi.fn>).mockReset()
  })

  it("lists schedule, user, next run and the last run's status", async () => {
    setup([
      job({
        name: "ok",
        last_run: { id: 1, source: "schedule", status: "succeeded", exit_code: 0, started_at: "2026-11-17T03:00:00Z" },
      }),
      job({
        id: 2,
        name: "broken",
        schedule: "*/5 * * * *",
        user: "www",
        last_run: { id: 2, source: "schedule", status: "failed", exit_code: 2, started_at: "2026-11-17T03:00:00Z" },
      }),
      job({ id: 3, name: "paused", enabled: false, next_run: null }),
    ])
    const broken = await row("broken")
    expect(broken.getByText("*/5 * * * *")).toBeInTheDocument()
    expect(broken.getByText("www")).toBeInTheDocument()
    expect(broken.getByText("Failed")).toBeInTheDocument()
    expect((await row("ok")).getByText("Succeeded")).toBeInTheDocument()
    const paused = await row("paused")
    expect(paused.getByText("Disabled")).toBeInTheDocument()
    expect(paused.getByText("Never run")).toBeInTheDocument()
    expect(paused.getByRole("button", { name: "Enable paused" })).toBeInTheDocument()
  })

  it("explains an empty list", async () => {
    setup([])
    expect(await screen.findByText("No cron jobs yet")).toBeInTheDocument()
  })

  it("reports what Run now did, including a skipped run", async () => {
    setup([job()])
    const user = userEvent.setup()
    vi.mocked(api.runCronJob).mockResolvedValueOnce({ outcome: "started", run: run({ status: "running" }) })
    await user.click(await screen.findByRole("button", { name: "Run backup now" }))
    expect(await screen.findByRole("status")).toHaveTextContent(/backup started/)
    expect(api.runCronJob).toHaveBeenCalledWith(1)

    vi.mocked(api.runCronJob).mockResolvedValueOnce({ outcome: "skipped", run: run({ status: "skipped" }) })
    await user.click(screen.getByRole("button", { name: "Run backup now" }))
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(/overlap policy skips/))
  })

  it("enables and disables with one click", async () => {
    setup([job()])
    const user = userEvent.setup()
    vi.mocked(api.setCronEnabled).mockResolvedValue(job({ enabled: false, next_run: null }))
    await user.click(await screen.findByRole("button", { name: "Disable backup" }))
    expect(api.setCronEnabled).toHaveBeenCalledWith(1, false)
  })

  it("asks before deleting and says what goes with the job", async () => {
    setup([job()])
    const user = userEvent.setup()
    vi.mocked(api.deleteCronJob).mockResolvedValue(undefined)
    await user.click(await screen.findByRole("button", { name: "Delete backup" }))
    const dialog = await screen.findByRole("dialog")
    expect(dialog).toHaveTextContent(/run history and log files are removed/)
    expect(api.deleteCronJob).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole("button", { name: "Delete job" }))
    await waitFor(() => expect(api.deleteCronJob).toHaveBeenCalledWith(1))
  })

  it("creates a program job from the modal", async () => {
    setup([])
    const user = userEvent.setup()
    vi.mocked(api.createCronJob).mockResolvedValue(job())
    await user.click(await screen.findByRole("button", { name: "Add job" }))
    const dialog = await screen.findByRole("dialog")

    await user.type(within(dialog).getByLabelText("Name"), "nightly")
    await user.type(within(dialog).getByLabelText("Schedule"), "0 3 * * *")
    await user.type(within(dialog).getByLabelText("Command and arguments"), "/usr/bin/php{enter}/srv/artisan")
    await user.click(within(dialog).getByRole("button", { name: "Create job" }))

    await waitFor(() => expect(api.createCronJob).toHaveBeenCalled())
    expect(api.createCronJob).toHaveBeenCalledWith({
      name: "nightly",
      schedule: "0 3 * * *",
      kind: "argv",
      user: "root",
      overlap: "skip",
      enabled: true,
      argv: ["/usr/bin/php", "/srv/artisan"],
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
  })

  it("shows a rejected field under that field", async () => {
    setup([])
    const user = userEvent.setup()
    vi.mocked(api.createCronJob).mockRejectedValue(
      new ApiError(400, "invalid_input", "schedule: expected 5 fields", { field: "schedule" }),
    )
    await user.click(await screen.findByRole("button", { name: "Add job" }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Name"), "x")
    await user.type(within(dialog).getByLabelText("Schedule"), "daily")
    await user.click(within(dialog).getByRole("button", { name: "Create job" }))
    const field = within(dialog).getByLabelText("Schedule")
    expect(await within(dialog).findByText("expected 5 fields")).toBeInTheDocument()
    expect(field).toHaveAttribute("aria-invalid", "true")
  })

  it("turns the shell option off with an explanation while cron.allow_shell is off", async () => {
    setup([], false)
    const user = userEvent.setup()
    await user.click(await screen.findByRole("button", { name: "Add job" }))
    const dialog = await screen.findByRole("dialog")
    const option = await within(dialog).findByRole("option", { name: /Shell script/ })
    await waitFor(() => expect(option).toBeDisabled())
    expect(within(dialog).getByTestId("shell-note")).toHaveTextContent("cron.allow_shell")
    expect(within(dialog).getByRole("link", { name: "Settings page" })).toHaveAttribute("href", "/settings")
  })

  it("handles a 403 shell_not_allowed without losing the form", async () => {
    // The setting could not be read, so the option is offered and the server decides.
    setup([], null)
    const user = userEvent.setup()
    vi.mocked(api.createCronJob).mockRejectedValue(
      new ApiError(403, "shell_not_allowed", "Shell jobs are turned off."),
    )
    await user.click(await screen.findByRole("button", { name: "Add job" }))
    const dialog = await screen.findByRole("dialog")
    await user.type(within(dialog).getByLabelText("Name"), "sh")
    await user.type(within(dialog).getByLabelText("Schedule"), "* * * * *")
    await user.selectOptions(within(dialog).getByLabelText("What it runs"), "shell")
    await user.type(within(dialog).getByLabelText("Script"), "date")
    await user.click(within(dialog).getByRole("button", { name: "Create job" }))

    await waitFor(() => expect(api.createCronJob).toHaveBeenCalled())
    expect(vi.mocked(api.createCronJob).mock.calls[0]?.[0]).toMatchObject({ kind: "shell", script_body: "date" })
    expect(within(dialog).getByTestId("shell-note")).toHaveTextContent("it is off now")
    expect(within(dialog).getByLabelText("Name")).toHaveValue("sh")
    expect(dialog).toBeInTheDocument()
  })

  it("edits without sending the name", async () => {
    setup([job()])
    const user = userEvent.setup()
    vi.mocked(api.updateCronJob).mockResolvedValue(job())
    await user.click(await screen.findByRole("button", { name: "Edit backup" }))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByLabelText("Name")).toBeDisabled()
    await user.click(within(dialog).getByRole("button", { name: "Save changes" }))
    await waitFor(() => expect(api.updateCronJob).toHaveBeenCalled())
    const [id, input] = vi.mocked(api.updateCronJob).mock.calls[0] ?? []
    expect(id).toBe(1)
    expect(input).not.toHaveProperty("name")
    expect(input).toMatchObject({ kind: "argv", argv: ["/usr/bin/true"], timeout_seconds: 3600, retention: 50 })
  })

  it("shows the run history and the end of a run's output", async () => {
    setup([job()])
    const user = userEvent.setup()
    vi.mocked(api.cronRuns).mockResolvedValue([
      run({ id: 12, status: "failed", exit_code: 3, output_tail: "boom\n", error: "exit status 3" }),
      run(),
    ])
    vi.mocked(api.cronRun).mockImplementation(async (_j, id) =>
      id === 12 ? run({ id: 12, status: "failed", exit_code: 3, output_tail: "boom\n", truncated: true, log_path: "/logs/backup/12.log" }) : run(),
    )
    await user.click(await screen.findByRole("button", { name: "History of backup" }))
    const dialog = await screen.findByRole("dialog")
    const rows = await within(dialog).findAllByTestId("run-row")
    expect(rows).toHaveLength(2)
    expect(within(rows[0]!).getByText("Failed")).toBeInTheDocument()
    expect(within(rows[0]!).getByText("3")).toBeInTheDocument()
    expect(within(rows[0]!).getByText("9s")).toBeInTheDocument()

    await user.click(within(rows[0]!).getByRole("button", { name: /Output of the run/ }))
    expect(await within(dialog).findByLabelText("Output of run 12")).toHaveTextContent("boom")
    expect(within(dialog).getByText(/Only the last 8 KiB is shown/)).toHaveTextContent("/logs/backup/12.log")
    expect(api.cronRuns).toHaveBeenCalledWith(1, 30)
  })
})
