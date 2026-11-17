import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { CreateSiteForm } from "@/components/CreateSiteDialog"
import { PoolForm } from "@/components/SitePHP"
import { ApiError, type PHPSettings, type PHPVersion, type PoolSettings } from "@/lib/api"
import { describeVersion, PHPSettingsForm } from "@/pages/PHPPage"

const settings: PHPSettings = {
  memory_limit: "256M",
  upload_max_filesize: "64M",
  post_max_size: "64M",
  max_execution_time: 60,
  date_timezone: "UTC",
}

const pool: PoolSettings = {
  pm: "dynamic",
  max_children: 5,
  start_servers: 2,
  min_spare_servers: 1,
  max_spare_servers: 3,
  max_requests: 500,
  idle_timeout: 10,
}

const version = (over: Partial<PHPVersion>): PHPVersion => ({
  version: "8.3",
  image: "php:8.3-fpm",
  digest: "",
  state: "ready",
  output: "",
  updated_at: null,
  supported: true,
  container: { exists: false, running: false, status: "missing" },
  sites: [],
  current: null,
  ...over,
})

describe("describeVersion", () => {
  it("tells an installed version nobody uses from one that should be running", () => {
    expect(describeVersion(version({})).badge).toBe("installed")
    expect(describeVersion(version({})).ok).toBe(true)

    const down = describeVersion(version({ sites: ["a.test"] }))
    expect(down.badge).toBe("stopped")
    expect(down.ok).toBe(false)

    const up = describeVersion(
      version({ sites: ["a.test", "b.test"], container: { exists: true, running: true, status: "running" } }),
    )
    expect(up).toEqual({ badge: "running", ok: true, text: "Serving 2 sites." })
  })

  it("reports the image states", () => {
    expect(describeVersion(version({ state: "absent" })).badge).toBe("not installed")
    expect(describeVersion(version({ state: "pulling" })).badge).toBe("pulling")
    expect(describeVersion(version({ state: "failed" })).ok).toBe(false)
  })
})

describe("PHPSettingsForm", () => {
  const renderForm = (error: unknown = null) => {
    const onSave = vi.fn()
    render(
      <PHPSettingsForm version="8.3" settings={settings} pending={false} error={error} onSave={onSave} />,
    )
    return onSave
  }

  it("saves only once something changed, with the edited values", async () => {
    const user = userEvent.setup()
    const onSave = renderForm()
    const save = screen.getByRole("button", { name: "Save and reload" })
    expect(save).toBeDisabled()

    const memory = screen.getByLabelText("Memory per request")
    await user.clear(memory)
    await user.type(memory, "512M")
    await user.click(save)

    expect(onSave).toHaveBeenCalledWith({ ...settings, memory_limit: "512M" }, expect.any(Function))
  })

  it("shows a rejected value under its field", () => {
    renderForm(
      new ApiError(422, "invalid_input", "memory_limit: must be digits with an optional K, M or G", {
        field: "memory_limit",
      }),
    )
    const memory = screen.getByLabelText("Memory per request")
    expect(memory).toHaveAttribute("aria-invalid", "true")
    expect(screen.getByRole("alert")).toHaveTextContent("must be digits with an optional K, M or G")
  })

  it("shows PHP's own output when it rejected the configuration", () => {
    renderForm(
      new ApiError(422, "config_rejected", "PHP 8.3 rejected the configuration; the previous files are back in place", {
        output: "PHP:  syntax error, unexpected '=' in php.ini on line 3\n",
      }),
    )
    expect(screen.getByTestId("validator-output")).toHaveTextContent("syntax error, unexpected '='")
  })
})

describe("PoolForm", () => {
  const renderForm = (initial: PoolSettings = pool) => {
    const onSave = vi.fn()
    render(<PoolForm settings={initial} pending={false} error={null} onSave={onSave} />)
    return onSave
  }

  it("shows only the fields the chosen mode uses", async () => {
    const user = userEvent.setup()
    renderForm()
    expect(screen.getByLabelText("Fewest idle workers")).toBeInTheDocument()
    expect(screen.queryByLabelText("Idle seconds before a worker stops")).not.toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText("How workers are managed"), "ondemand")
    expect(screen.queryByLabelText("Fewest idle workers")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Idle seconds before a worker stops")).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText("How workers are managed"), "static")
    expect(screen.queryByLabelText("Idle seconds before a worker stops")).not.toBeInTheDocument()
    expect(screen.getByLabelText("Most workers")).toBeInTheDocument()
  })

  it("keeps the hidden values when the mode changes", async () => {
    const user = userEvent.setup()
    const onSave = renderForm()
    await user.selectOptions(screen.getByLabelText("How workers are managed"), "static")
    await user.click(screen.getByRole("button", { name: "Save and reload" }))
    expect(onSave).toHaveBeenCalledWith({ ...pool, pm: "static" }, expect.any(Function))
  })
})

describe("CreateSiteForm, PHP", () => {
  const renderForm = (phpVersions: string[]) => {
    const onSubmit = vi.fn()
    render(
      <CreateSiteForm
        onSubmit={onSubmit}
        onCancel={() => {}}
        pending={false}
        error={null}
        phpVersions={phpVersions}
      />,
    )
    return onSubmit
  }

  it("submits the newest installed version by default, without phpinfo", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm(["8.3", "8.4"])
    await user.click(screen.getByRole("radio", { name: /A PHP application/ }))
    await user.type(screen.getByLabelText("Domain"), "app.test")
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "app.test",
      aliases: [],
      kind: "php",
      php_version: "8.4",
    })
  })

  it("sends the chosen version and the phpinfo page when ticked", async () => {
    const user = userEvent.setup()
    const onSubmit = renderForm(["8.3", "8.4"])
    await user.click(screen.getByRole("radio", { name: /A PHP application/ }))
    await user.type(screen.getByLabelText("Domain"), "app.test")
    await user.selectOptions(screen.getByLabelText("PHP version"), "8.3")
    await user.click(screen.getByRole("checkbox", { name: /Start with a phpinfo\(\) page/ }))
    await user.click(screen.getByRole("button", { name: "Create site" }))

    expect(onSubmit).toHaveBeenCalledWith({
      domain: "app.test",
      aliases: [],
      kind: "php",
      php_version: "8.3",
      phpinfo: true,
    })
  })

  it("cannot create a PHP site before a version is installed", async () => {
    const user = userEvent.setup()
    renderForm([])
    await user.click(screen.getByRole("radio", { name: /A PHP application/ }))
    await user.type(screen.getByLabelText("Domain"), "app.test")

    expect(screen.getByRole("status")).toHaveTextContent("No PHP version is installed yet")
    expect(screen.getByRole("button", { name: "Create site" })).toBeDisabled()
  })
})
