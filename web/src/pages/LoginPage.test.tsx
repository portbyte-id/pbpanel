import { createHash } from "node:crypto"

import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { api, ApiError } from "@/lib/api"
import { LoginPage } from "@/pages/LoginPage"

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
}))

vi.mock("@/lib/api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api")>()
  return { ...actual, api: { ...actual.api, login: vi.fn(), loginChallenge: vi.fn() } }
})

// A low difficulty keeps the in-browser solve quick under test.
const PUZZLE = { required: true, challenge: "test-puzzle.1.4.mac", difficulty: 4 }

function renderPage() {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  render(
    <QueryClientProvider client={client}>
      <LoginPage />
    </QueryClientProvider>,
  )
}

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText("Username"), "admin")
  await user.type(screen.getByLabelText("Password"), "wrong password")
  await user.click(screen.getByRole("button", { name: "Sign in" }))
}

describe("LoginPage", () => {
  beforeEach(() => {
    vi.mocked(api.login).mockReset()
    vi.mocked(api.loginChallenge).mockReset()
  })

  it("shows no check to a client that has not failed a login", async () => {
    vi.mocked(api.loginChallenge).mockResolvedValue({ required: false })
    renderPage()

    await waitFor(() => expect(api.loginChallenge).toHaveBeenCalledTimes(1))
    expect(screen.queryByRole("checkbox", { name: "I am not a robot" })).toBeNull()
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled()
  })

  it("puts the check up at once after a failed login, unticked, and blocks sign-in", async () => {
    const user = userEvent.setup()
    vi.mocked(api.loginChallenge).mockResolvedValue({ required: false })
    vi.mocked(api.login).mockRejectedValue(
      new ApiError(401, "invalid_credentials", "username or password is incorrect"),
    )
    renderPage()

    await fillAndSubmit(user)

    const box = await screen.findByRole("checkbox", { name: "I am not a robot" })
    expect(box).not.toBeChecked()
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled()
    expect(screen.getByRole("alert")).toHaveTextContent("username or password is incorrect")
  })

  it("does not start solving until the visitor ticks the box", async () => {
    const user = userEvent.setup()
    vi.mocked(api.loginChallenge).mockResolvedValue({ required: false })
    vi.mocked(api.login).mockRejectedValue(new ApiError(401, "invalid_credentials", "nope"))
    renderPage()

    await fillAndSubmit(user)
    await screen.findByRole("checkbox", { name: "I am not a robot" })

    // Only the mount-time lookup so far: the puzzle is not requested on render.
    expect(api.loginChallenge).toHaveBeenCalledTimes(1)
    expect(api.login).toHaveBeenCalledTimes(1)
  })

  it("solves after the box is ticked and sends the answer with the next attempt", async () => {
    const user = userEvent.setup()
    vi.mocked(api.loginChallenge).mockResolvedValueOnce({ required: false }).mockResolvedValue(PUZZLE)
    vi.mocked(api.login)
      .mockRejectedValueOnce(new ApiError(401, "invalid_credentials", "nope"))
      .mockResolvedValueOnce({ username: "admin", csrf_token: "t" })
    renderPage()

    await fillAndSubmit(user)
    const box = await screen.findByRole("checkbox", { name: "I am not a robot" })

    await user.click(box)
    await waitFor(() => expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled())
    expect(api.loginChallenge).toHaveBeenCalledTimes(2)

    await user.click(screen.getByRole("button", { name: "Sign in" }))
    await waitFor(() => expect(api.login).toHaveBeenCalledTimes(2))

    const check = vi.mocked(api.login).mock.calls[1][2]
    expect(check?.challenge).toBe(PUZZLE.challenge)
    const digest = createHash("sha256").update(`${PUZZLE.challenge}:${check?.nonce}`).digest("hex")
    expect(digest.startsWith("0")).toBe(true)
  })

  it("unticking the box before the answer is found leaves sign-in blocked", async () => {
    const user = userEvent.setup()
    vi.mocked(api.loginChallenge)
      .mockResolvedValueOnce({ required: true })
      .mockResolvedValue({ ...PUZZLE, difficulty: 60 })
    vi.mocked(api.login).mockRejectedValue(
      new ApiError(403, "challenge_required", "complete the check to continue"),
    )
    renderPage()

    await fillAndSubmit(user)
    const box = await screen.findByRole("checkbox", { name: "I am not a robot" })
    await user.click(box)
    await waitFor(() => expect(api.loginChallenge).toHaveBeenCalledTimes(2))
    await user.click(box)

    expect(box).not.toBeChecked()
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled()
  })
})
