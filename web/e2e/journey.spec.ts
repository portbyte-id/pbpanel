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

  // 2. Log in.
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()

  // 3. The guard let us through and the landing page is the dashboard; Settings is one click away.
  await expect(page).toHaveURL(/\/dashboard/)
  await page.getByRole("link", { name: "Settings" }).click()
  await expect(page).toHaveURL(/\/settings/)
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible()

  // The form is generated from the registry, so these rows exist because the
  // server sent them, not because the page hard-coded them.
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
  await page.reload()
  await expect(page.locator('[data-setting="login.max_attempts"] input')).toHaveValue(next)

  // 6. The write is in the audit trail, attributed to the admin account.
  await page.getByRole("link", { name: "Audit" }).click()
  await expect(page).toHaveURL(/\/audit/)
  await expect(page.getByRole("cell", { name: "PUT /api/settings" }).first()).toBeVisible()

  // 7. The sidebar offers the future sections as disabled controls, so nothing
  //    looks clickable that has no page behind it yet.
  // Files was the example until M3, and Apps until M5; TLS is still one.
  const tls = page.getByRole("button", { name: /TLS — not built yet/i })
  await expect(tls).toBeDisabled()
  // PHP was one of them until M2; it is a page now.
  await page.getByRole("link", { name: "PHP" }).click()
  await expect(page).toHaveURL(/\/php/)

  // 8. Log out. The session is revoked server-side, so the guard sends us back
  //    to the login page even on a fresh navigation.
  await page.getByRole("button", { name: "Sign out" }).click()
  await expect(page).toHaveURL(/\/login/)

  await page.goto("/settings")
  await expect(page).toHaveURL(/\/login/)
})
