import { expect, test } from "@playwright/test"

/**
 * The account flows: the login page starts blank, and both credential changes
 * live behind the header's account button.
 *
 * It runs against the real daemon (see playwright.config.ts), so the rename goes
 * through the session, the CSRF header and the store. The rename is undone at
 * the end so the other journeys still sign in as `admin`.
 */
const PASSWORD = "e2e-password-123"
const RENAMED = "operator"

async function signIn(page: import("@playwright/test").Page, username: string) {
  await page.getByLabel("Username").fill(username)
  await page.getByRole("textbox", { name: "Password" }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/dashboard/)
}

test("the login page starts with no username filled in", async ({ page }) => {
  await page.goto("/")
  await expect(page).toHaveURL(/\/login/)

  const username = page.getByLabel("Username")
  await expect(username).toHaveValue("")
  await expect(username).toBeFocused()
})

test("change the username, sign in under it, and change it back", async ({ page }) => {
  await page.goto("/")
  await signIn(page, "admin")

  // The account button is named after the signed-in account.
  await page.getByRole("button", { name: "Account: admin" }).click()
  await page.getByRole("button", { name: "Change username" }).click()

  const dialog = page.getByRole("dialog")
  await expect(dialog).toBeVisible()
  await dialog.getByLabel("New username").fill(RENAMED)
  await dialog.getByLabel("Current password").fill(PASSWORD)
  await dialog.getByRole("button", { name: "Change username" }).click()

  // Every session is revoked, so the panel sends the operator back to sign in.
  await expect(page).toHaveURL(/\/login/)

  await signIn(page, RENAMED)
  await expect(page.getByRole("button", { name: `Account: ${RENAMED}` })).toBeVisible()

  // Put the account back, so a later journey's `admin` login still works.
  await page.getByRole("button", { name: `Account: ${RENAMED}` }).click()
  await page.getByRole("button", { name: "Change username" }).click()
  const again = page.getByRole("dialog")
  await again.getByLabel("New username").fill("admin")
  await again.getByLabel("Current password").fill(PASSWORD)
  await again.getByRole("button", { name: "Change username" }).click()
  await expect(page).toHaveURL(/\/login/)

  await signIn(page, "admin")
})

test("the password dialog still opens from the account menu", async ({ page }) => {
  await page.goto("/")
  await signIn(page, "admin")

  await page.getByRole("button", { name: "Account: admin" }).click()
  await page.getByRole("button", { name: "Change password" }).click()

  const dialog = page.getByRole("dialog")
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole("heading", { name: "Change password" })).toBeVisible()

  // Left unsubmitted: a real change would revoke the session and rewrite the
  // password every later journey signs in with.
  await dialog.getByRole("button", { name: "Cancel" }).click()
  await expect(dialog).toBeHidden()
})
