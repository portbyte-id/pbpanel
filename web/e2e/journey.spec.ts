import { expect, test } from "@playwright/test"

/**
 * The foundation journey: log in, change a setting, log out.
 *
 * It runs against the real daemon (see playwright.config.ts), so it exercises
 * the whole path: the session cookie, the CSRF header on the PUT, the registry's
 * validation, and the audit entry that the write leaves behind.
 */
const PASSWORD = "e2e-password-123"

test("login, change a setting, log out", async ({ page }) => {
  // 1. An unauthenticated visit lands on the login page, not an error.
  await page.goto("/")
  await expect(page).toHaveURL(/\/login/)

  // 2. Log in. The password field has a "Show password" button beside it whose
  //    accessible name also contains "password", so the field is matched exactly.
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()

  // 3. The guard let us through and the landing page is the dashboard; Settings is one click away.
  await expect(page).toHaveURL(/\/dashboard/)
  await page.getByRole("link", { name: "Settings" }).click()
  await expect(page).toHaveURL(/\/settings/)
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible()

  // The form is generated from the registry, so these rows exist because the
  // server sent them, not because the page hard-coded them. Each group is a tab
  // and the first one is shown, so the Login group is opened by name.
  await page.getByRole("tab", { name: "Login" }).click()
  const maxAttempts = page.locator('[data-setting="login.max_attempts"] input')
  await expect(maxAttempts).toBeVisible()

  // 4. Change one setting and save. The save is a PUT carrying X-CSRF-Token;
  //    if that header were missing the server would answer 403 and the page
  //    would show the error instead of "Saved.".
  const before = await maxAttempts.inputValue()
  const next = before === "7" ? "8" : "7"
  await maxAttempts.fill(next)
  await page.getByRole("button", { name: "Save changes" }).click()
  await expect(page.getByText("Saved.")).toBeVisible()

  // 5. The change survived a reload: it was stored, not just held in the form.
  //    The page reopens on its first tab, so Login is selected again by name.
  await page.reload()
  await page.getByRole("tab", { name: "Login" }).click()
  await expect(page.locator('[data-setting="login.max_attempts"] input')).toHaveValue(next)

  // 6. The write is in the audit trail, attributed to the admin account.
  await page.getByRole("link", { name: "Audit" }).click()
  await expect(page).toHaveURL(/\/audit/)
  await expect(page.getByRole("cell", { name: "PUT /api/settings" }).first()).toBeVisible()

  // 7. The sidebar's last placeholder is gone: every section it lists now has a
  //    page behind it. The Store is the newest one, and the engine picker on a
  //    site's Firewall tab points at it, so it has to be reachable.
  await page.getByRole("link", { name: "Store" }).click()
  await expect(page).toHaveURL(/\/store/)
  await expect(page.getByRole("heading", { name: "Firewall engines" })).toBeVisible()
  // PHP was a placeholder until M2; it lives under Runtimes now.
  await page.getByRole("link", { name: "Runtimes" }).click()
  await page.getByRole("tab", { name: "PHP" }).click()
  await expect(page).toHaveURL(/\/runtimes\?tab=php/)

  // 8. Log out. The session is revoked server-side, so the guard sends us back
  //    to the login page even on a fresh navigation. Sign out is in the account
  //    menu in the header, opened by the button named for the signed-in user.
  await page.getByRole("button", { name: /^Account: / }).click()
  await page.getByRole("button", { name: "Sign out" }).click()
  await expect(page).toHaveURL(/\/login/)

  await page.goto("/settings")
  await expect(page).toHaveURL(/\/login/)
})
