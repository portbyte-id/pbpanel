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
  // The "Show password" button's name also contains "password", so exact.
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/dashboard/)
}

test("add an app, control it, open the proxy-site shortcut, see a refused file", async ({ page }) => {
  await login(page)

  // 1. Apps are a tab of the Sites page now, not a page of their own: "Other
  //    programs" is where a plain binary lives.
  await page.getByRole("link", { name: "Sites" }).click()
  await expect(page).toHaveURL(/\/sites/)
  await page.getByRole("tab", { name: "Binary" }).click()
  await expect(page.getByText("No other programs yet.")).toBeVisible()

  // 2. Add a program with a port. It autostarts, so it is running when the list refreshes.
  await page.getByRole("button", { name: "Add app" }).click()
  await page.getByLabel("Name").fill("e2e-app")
  await page.getByLabel("Execution command").fill("/bin/e2e-app")
  await page.getByLabel("Port").fill("3011")
  await page.getByRole("button", { name: "Create app" }).click()
  const row = page.getByRole("row", { name: /e2e-app/ })
  await expect(row).toContainText("RUNNING")
  await expect(row).toContainText("3011")

  // 3. Stop it, then start it again. Starting opens the app's log in a dialog
  //    (a start that fails has to be readable), so the dialog is closed before
  //    the row is read again.
  await row.getByRole("button", { name: "Stop", exact: true }).click()
  await expect(row).toContainText("STOPPED")
  // "Start" would also match "Restart" by substring.
  await row.getByRole("button", { name: "Start", exact: true }).click()
  await expect(page.getByRole("dialog")).toBeVisible()
  await page.keyboard.press("Escape")
  await expect(page.getByRole("dialog")).toBeHidden()
  await expect(row).toContainText("RUNNING")

  // 4. "Manage" opens the app's dialog; its Domains section adds a site that
  //    passes requests to the app's address, with the target already filled in.
  await row.getByRole("button", { name: "Manage" }).click()
  const appDialog = page.getByRole("dialog")
  await expect(appDialog).toBeVisible()
  await appDialog.getByRole("button", { name: "Domains" }).click()
  await appDialog.getByRole("button", { name: "Add domain" }).click()
  await expect(page.getByRole("heading", { name: "Add a site" })).toBeVisible()
  // The dialog no longer asks for a "kind": a filled route is what makes it a
  // proxy site, and the app's address is already in the route.
  await expect(page.getByLabel("Pass requests to")).toHaveValue("http://127.0.0.1:3011")

  // Closing the site dialog leaves the app's dialog open; closing that one too
  // returns to the table. Each close is waited for before the next, because
  // Escape and the Close button act on whichever dialog holds focus.
  await page.keyboard.press("Escape")
  await expect(page.getByRole("heading", { name: "Add a site" })).toBeHidden()
  await expect(appDialog).toBeVisible()
  await appDialog.getByRole("button", { name: "Close" }).click()
  await expect(page.getByRole("dialog")).toHaveCount(0)

  // 5. A program file supervisord refuses is shown verbatim and nothing is saved.
  await page.getByRole("tab", { name: "Binary" }).click()
  await page.getByRole("button", { name: "Add app" }).click()
  await page.getByLabel("Name").fill("bad")
  await page.getByLabel("Execution command").fill("/bin/bogus_directive")
  await page.getByRole("button", { name: "Create app" }).click()
  await expect(page.getByTestId("validator-output")).toContainText("unknown program option bogus_directive")
  await page.getByRole("button", { name: "Cancel" }).click()
  await expect(page.getByRole("row", { name: /bad/ })).toHaveCount(0)
})
