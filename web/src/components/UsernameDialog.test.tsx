import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { UsernameDialog } from "@/components/UsernameDialog"
import { api, ApiError } from "@/lib/api"

const navigate = vi.fn()

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { ...actual.api, changeUsername: vi.fn() } }
})

function renderDialog(onOpenChange = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <UsernameDialog username="admin" open onOpenChange={onOpenChange} />
    </QueryClientProvider>,
  )
  return onOpenChange
}

describe("UsernameDialog", () => {
  beforeEach(() => {
    vi.mocked(api.changeUsername).mockReset()
    navigate.mockReset()
  })

  it("sends the trimmed name with the current password and signs out", async () => {
    const user = userEvent.setup()
    vi.mocked(api.changeUsername).mockResolvedValue(undefined)
    const onOpenChange = renderDialog()

    const field = screen.getByLabelText("New username")
    await user.clear(field)
    await user.type(field, "  operator  ")
    await user.type(screen.getByLabelText("Current password"), "the current password")
    await user.click(screen.getByRole("button", { name: "Change username" }))

    await waitFor(() => {
      expect(api.changeUsername).toHaveBeenCalledWith("the current password", "operator")
    })
    await waitFor(() => {
      expect(navigate).toHaveBeenCalledWith({ to: "/login", replace: true })
    })
    expect(onOpenChange).toHaveBeenCalledWith(false)
  })

  it("refuses to submit the name already in use, without calling the server", async () => {
    const user = userEvent.setup()
    renderDialog()

    await user.type(screen.getByLabelText("Current password"), "the current password")
    await user.click(screen.getByRole("button", { name: "Change username" }))

    expect(await screen.findByRole("alert")).toHaveTextContent("That is the current username.")
    expect(api.changeUsername).not.toHaveBeenCalled()
  })

  it("shows the server's own sentence when the password is wrong", async () => {
    const user = userEvent.setup()
    vi.mocked(api.changeUsername).mockRejectedValue(
      new ApiError(401, "invalid_credentials", "username or password is incorrect"),
    )
    renderDialog()

    const field = screen.getByLabelText("New username")
    await user.clear(field)
    await user.type(field, "operator")
    await user.type(screen.getByLabelText("Current password"), "wrong")
    await user.click(screen.getByRole("button", { name: "Change username" }))

    expect(await screen.findByRole("alert")).toHaveTextContent("username or password is incorrect")
    expect(navigate).not.toHaveBeenCalled()
  })
})
