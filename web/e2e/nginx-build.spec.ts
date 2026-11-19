import { expect, type Page, test } from "@playwright/test"

/**
 * The nginx Build tab: the version box takes a series or a full release, and the
 * serving build is named by the release it compiled, patch level included.
 *
 * The HTTP layer, the route and the SPA are the real ones. The build manager is the
 * stand-in in scripts/e2e/server.go, which records the version it was asked for
 * instead of compiling for 5 to 8 minutes; a real build is covered by
 * scripts/smoke/7-native-nginx.sh.
 */
const PASSWORD = "e2e-password-123"

async function login(page: Page) {
  await page.goto("/login")
  await page.getByRole("textbox", { name: "Username" }).fill("admin")
  await page.getByRole("textbox", { name: "Password" }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/dashboard/)
}

test("type an nginx release and see the compiled patch level", async ({ page }) => {
  await login(page)

  await page.getByRole("link", { name: "Nginx" }).click()
  await page.getByRole("tab", { name: "Build" }).click()

  // 1. The serving build is named by the release it compiled, not by its series.
  const summary = page.locator("dl")
  await expect(summary.getByText("nginx 1.30.2")).toBeVisible()
  await expect(summary.getByText("nginx 1.29.4")).toBeVisible()
  // The history names each build's release too.
  const history = page.getByRole("list", { name: "Build history" })
  await expect(history.getByText("nginx 1.30.2")).toBeVisible()

  // 2. The version is typed, not picked, and it starts empty.
  const input = page.getByLabel("nginx version")
  await expect(input).toHaveValue("")

  // 3. A release the server would refuse disables the button and says why.
  await input.fill("1.2.3.4")
  await expect(page.getByRole("button", { name: "Switch version" })).toBeDisabled()
  await expect(page.getByText(/Use a version such as 1\.30, or a full release/)).toBeVisible()

  // 4. A full release with its patch is accepted.
  await input.fill("1.30.1")
  const button = page.getByRole("button", { name: "Switch version" })
  await expect(button).toBeEnabled()
  await button.click()

  // 5. The switch started: the box clears and the job drawer opens.
  await expect(input).toHaveValue("")
  await expect(page.getByText("Switching nginx to 1.30.1")).toBeVisible()

  // 6. The API took the release verbatim, not the series it belongs to.
  const answer = await page.request.get("/api/nginx/image")
  expect((await answer.json()).version).toBe("1.30.1")
})
