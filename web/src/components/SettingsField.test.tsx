import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it, vi } from "vitest"

import { SettingField } from "@/components/SettingsField"
import { mapSettingError } from "@/pages/SettingsPage"
import { ApiError, type SettingKey } from "@/lib/api"

/** One registry key of each type the Go registry can produce. */
const keys: Record<string, SettingKey> = {
  port: {
    name: "panel.port",
    type: "port",
    value: "8443",
    default: "8443",
    is_default: true,
    restart_required: true,
    description: "HTTPS port the panel listens on.",
    group: "Panel",
  },
  text: {
    name: "panel.bind",
    type: "string",
    value: "0.0.0.0",
    default: "0.0.0.0",
    is_default: true,
    restart_required: true,
    description: "Address the panel listens on.",
    group: "Panel",
  },
  integer: {
    name: "login.max_attempts",
    type: "int",
    value: "5",
    default: "5",
    is_default: true,
    restart_required: false,
    description: "Failed logins allowed from one address.",
    group: "Login",
  },
  duration: {
    name: "login.lockout",
    type: "duration",
    value: "15m",
    default: "15m",
    is_default: true,
    restart_required: false,
    description: "How long an address stays locked out.",
    group: "Login",
  },
  flag: {
    name: "panel.debug",
    type: "bool",
    value: "false",
    default: "false",
    is_default: true,
    restart_required: false,
    description: "Verbose logging.",
    group: "Panel",
  },
  path: {
    name: "panel.data_dir",
    type: "path",
    value: "/www/panel/data",
    default: "/www/panel/data",
    is_default: true,
    restart_required: true,
    description: "Where the database lives.",
    group: "Panel",
  },
  choice: {
    name: "panel.log_level",
    type: "enum",
    value: "info",
    default: "info",
    is_default: true,
    restart_required: false,
    description: "Log verbosity.",
    group: "Panel",
    allowed: ["debug", "info", "warn", "error"],
  },
}

/** The control inside a key's field wrapper. */
function controlFor(setting: SettingKey): HTMLElement {
  const field = document.querySelector(`[data-setting="${setting.name}"]`)
  if (!field) throw new Error(`no field wrapper for ${setting.name}`)
  const control = field.querySelector<HTMLElement>("input, select")
  if (!control) throw new Error(`no control for ${setting.name}`)
  return control
}

describe("settings form rendering by key type", () => {
  it("renders a port key as a bounded number input", () => {
    render(<SettingField setting={keys.port} value="8443" onChange={() => {}} />)
    const input = controlFor(keys.port) as HTMLInputElement

    expect(input.type).toBe("number")
    expect(input.min).toBe("1")
    expect(input.max).toBe("65535")
    expect(input.value).toBe("8443")
  })

  it("renders an int key as a number input", () => {
    render(<SettingField setting={keys.integer} value="5" onChange={() => {}} />)
    const input = controlFor(keys.integer) as HTMLInputElement

    expect(input.type).toBe("number")
    expect(input.step).toBe("1")
    expect(input.value).toBe("5")
  })

  it("renders a string key as a text input", () => {
    render(<SettingField setting={keys.text} value="0.0.0.0" onChange={() => {}} />)
    const input = controlFor(keys.text) as HTMLInputElement

    expect(input.type).toBe("text")
    expect(input.value).toBe("0.0.0.0")
  })

  it("renders a path key as a text input holding the path", () => {
    render(<SettingField setting={keys.path} value="/www/panel/data" onChange={() => {}} />)
    const input = controlFor(keys.path) as HTMLInputElement

    expect(input.type).toBe("text")
    expect(input.value).toBe("/www/panel/data")
  })

  it("renders a duration key as a text input with the format hint", () => {
    render(<SettingField setting={keys.duration} value="15m" onChange={() => {}} />)
    const input = controlFor(keys.duration) as HTMLInputElement

    expect(input.type).toBe("text")
    expect(input.value).toBe("15m")
    expect(screen.getByText("e.g. 90s, 30m, 2h")).toBeInTheDocument()
  })

  it("renders a bool key as a two-option select", () => {
    render(<SettingField setting={keys.flag} value="false" onChange={() => {}} />)
    const select = controlFor(keys.flag) as HTMLSelectElement

    expect(select.tagName).toBe("SELECT")
    expect(select.value).toBe("false")
    expect(within(select).getByRole("option", { name: "Enabled" })).toBeInTheDocument()
    expect(within(select).getByRole("option", { name: "Disabled" })).toBeInTheDocument()
  })

  it("renders an enum key with exactly the registry's allowed values", () => {
    render(<SettingField setting={keys.choice} value="info" onChange={() => {}} />)
    const select = controlFor(keys.choice) as HTMLSelectElement

    expect(select.tagName).toBe("SELECT")
    expect(select.value).toBe("info")
    const options = Array.from(select.options).map((o) => o.value)
    expect(options).toEqual(["debug", "info", "warn", "error"])
  })
})

describe("settings form metadata", () => {
  it("shows the restart-required badge only where the registry asks for it", () => {
    const { unmount } = render(
      <SettingField setting={keys.port} value="8443" onChange={() => {}} />,
    )
    expect(screen.getByText("restart required")).toBeInTheDocument()
    unmount()

    render(<SettingField setting={keys.integer} value="5" onChange={() => {}} />)
    expect(screen.queryByText("restart required")).not.toBeInTheDocument()
  })

  it("shows the registry's description under the key", () => {
    render(<SettingField setting={keys.integer} value="5" onChange={() => {}} />)
    expect(screen.getByText("Failed logins allowed from one address.")).toBeInTheDocument()
  })

  it("reports edits through onChange", async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(<SettingField setting={keys.integer} value="5" onChange={onChange} />)

    // The field is controlled by its parent, so the typed value is not echoed
    // back here: what this asserts is that the edit leaves through onChange.
    await user.type(controlFor(keys.integer), "9")

    expect(onChange).toHaveBeenCalled()
    expect(onChange.mock.calls.at(-1)?.[0]).toBe("59")
  })

  it("offers a reset control only for a key that is not at its default", () => {
    const changed: SettingKey = { ...keys.port, value: "9443", is_default: false }
    render(<SettingField setting={changed} value="9443" onChange={() => {}} />)

    expect(screen.getByRole("button", { name: /reset panel.port/i })).toBeInTheDocument()
  })

  it("hides the reset control at the default value", () => {
    render(<SettingField setting={keys.port} value="8443" onChange={() => {}} />)

    expect(screen.queryByRole("button", { name: /reset panel.port/i })).not.toBeInTheDocument()
  })
})

describe("settings form validator errors", () => {
  it("shows the server's sentence inline and marks the control invalid", () => {
    render(
      <SettingField setting={keys.integer} value="5" error="must be between 1 and 1000" onChange={() => {}} />,
    )

    const alert = screen.getByRole("alert")
    expect(alert).toHaveTextContent("must be between 1 and 1000")

    const input = controlFor(keys.integer)
    expect(input).toHaveAttribute("aria-invalid", "true")
    expect(input.getAttribute("aria-describedby")).toBe(alert.id)
  })

  it("shows no alert when the key is accepted", () => {
    render(<SettingField setting={keys.integer} value="5" onChange={() => {}} />)
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
  })
})

describe("mapSettingError", () => {
  const writes = [
    { name: "login.max_attempts", value: "0" },
    { name: "panel.port", value: "99999" },
  ]

  it("attributes the rejection to the key the server named", () => {
    const error = new ApiError(
      400,
      "invalid_setting",
      "settings[1] (panel.port): port must be between 1 and 65535",
    )

    expect(mapSettingError(error, writes)).toEqual({
      fieldErrors: { "panel.port": "port must be between 1 and 65535" },
    })
  })

  it("leaves a failure of another class at form level", () => {
    const error = new ApiError(500, "internal_error", "the request could not be completed")
    expect(mapSettingError(error, writes)).toEqual({ fieldErrors: {} })
  })

  it("leaves an unattributable detail at form level", () => {
    const error = new ApiError(400, "invalid_setting", "something else went wrong")
    expect(mapSettingError(error, writes)).toEqual({ fieldErrors: {} })
  })
})
