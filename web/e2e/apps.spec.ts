import { expect, type Page, test } from "@playwright/test"

/**
 * The apps journey: add a program, stop and start it, follow the "Proxy site"
 * shortcut into the add-site dialog, and see supervisord's refusal verbatim.
 *
 * The HTTP chain and the apps manager are the real ones. supervisord is the
 * stand-in in scripts/e2e/server.go, which refuses any program file containing
 * `bogus_directive`; the real daemon is covered by scripts/smoke/5-apps.sh.
 */
const PASSWORD = "e2e-password-123"

async function login(page: Page) {
  await page.goto("/login")
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/settings/)
}

test("add an app, control it, open the proxy-site shortcut, see a refused file", async ({ page }) => {
  await login(page)

  // 1. The Apps section is a real page now, not a disabled entry.
  await page.getByRole("link", { name: "Apps" }).click()
  await expect(page).toHaveURL(/\/apps/)
  await expect(page.getByRole("heading", { name: "Apps" })).toBeVisible()
  await expect(page.getByText("No apps yet")).toBeVisible()

  // 2. Add a program with a port. It autostarts, so it is running when the list refreshes.
  await page.getByRole("button", { name: "Add app" }).click()
  await page.getByLabel("Name").fill("e2e-app")
  await page.getByLabel("Program path").fill("/bin/e2e-app")
  await page.getByLabel("Port").fill("3011")
  await page.getByRole("button", { name: "Create app" }).click()
  const row = page.getByRole("row", { name: /e2e-app/ })
  await expect(row).toContainText("RUNNING")
  await expect(row).toContainText("3011")

  // 3. Stop it, then start it again.
  await row.getByRole("button", { name: "Stop", exact: true }).click()
  await expect(row).toContainText("STOPPED")
  // "Start" would also match "Restart" by substring.
  await row.getByRole("button", { name: "Start", exact: true }).click()
  await expect(row).toContainText("RUNNING")

  // 4. "Proxy site" opens the add-site dialog on "Another server" with the app's address.
  await row.getByRole("link", { name: "Proxy site" }).click()
  await expect(page).toHaveURL(/\/sites\?proxy=/)
  await expect(page.getByRole("heading", { name: "Add a site" })).toBeVisible()
  await expect(page.getByRole("radio", { name: /Another server/ })).toBeChecked()
  await expect(page.getByLabel("Pass requests to")).toHaveValue("http://127.0.0.1:3011")

  // Closing the dialog drops the parameter, so a reload does not reopen it.
  await page.keyboard.press("Escape")
  await expect(page).not.toHaveURL(/proxy=/)

  // 5. A program file supervisord refuses is shown verbatim and nothing is saved.
  await page.getByRole("link", { name: "Apps" }).click()
  await page.getByRole("button", { name: "Add app" }).click()
  await page.getByLabel("Name").fill("bad")
  await page.getByLabel("Program path").fill("/bin/bogus_directive")
  await page.getByRole("button", { name: "Create app" }).click()
  await expect(page.getByTestId("validator-output")).toContainText("unknown program option bogus_directive")
  await page.getByRole("button", { name: "Cancel" }).click()
  await expect(page.getByRole("row", { name: /bad/ })).toHaveCount(0)
})
