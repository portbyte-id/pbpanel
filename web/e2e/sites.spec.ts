import { expect, type Page, test } from "@playwright/test"

/**
 * The sites journey: create a static site, break its config and see the
 * rollback error, detach a control by removing its include line, regenerate.
 *
 * The HTTP chain and the sites model are the real ones. nginx is the stand-in in
 * scripts/e2e/server.go, which refuses any file containing `bogus_directive`
 * with a message shaped like nginx's; real nginx is covered by the integration
 * test and the smoke script.
 */
const PASSWORD = "e2e-password-123"
const DOMAIN = "e2e-site.test"

async function login(page: Page) {
  await page.goto("/login")
  await page.getByLabel("Username").fill("admin")
  await page.getByLabel("Password").fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/dashboard/)
}

/** Replaces the whole document of the CodeMirror editor. */
async function setEditor(page: Page, text: string) {
  const editor = page.locator(".cm-content")
  await editor.click()
  await page.keyboard.press("ControlOrMeta+a")
  await page.keyboard.insertText(text)
}

async function editorText(page: Page): Promise<string> {
  // One .cm-line per line; innerText of the content would add blank lines.
  const lines = await page.locator(".cm-content .cm-line").allInnerTexts()
  return lines.join("\n")
}

test("create a site, break its config, detach a control, regenerate", async ({ page }) => {
  await login(page)

  // 1. Create a static site from the list page.
  await page.getByRole("link", { name: "Sites" }).click()
  await expect(page).toHaveURL(/\/sites$/)
  await page.getByRole("button", { name: "Add site" }).click()
  await page.getByLabel("Domain", { exact: true }).fill(DOMAIN)
  await page.getByLabel("Other domains").fill(`www.${DOMAIN}`)
  await page.getByRole("button", { name: "Create site" }).click()

  // 2. The create lands on the site, with every control connected.
  await expect(page).toHaveURL(new RegExp(`/sites/${DOMAIN}$`))
  await expect(page.getByRole("heading", { name: DOMAIN })).toBeVisible()
  await expect(page.locator('[data-feature="names"]').getByText("connected")).toBeVisible()
  await expect(page.getByLabel("Other domains")).toHaveValue(`www.${DOMAIN}`)

  // 3. Break the config. The save is refused, nginx's message is shown as it
  //    came, and the draft stays so it can be fixed.
  await page.getByRole("tab", { name: "Config" }).click()
  await expect(page.locator(".cm-content")).toContainText("server {")
  const original = await editorText(page)
  expect(original).toContain(`includes/${DOMAIN}/names.conf;`)

  await setEditor(page, "server {\n    bogus_directive on;\n}\n")
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByRole("alert")).toContainText("nginx rejected the configuration")
  await expect(page.getByTestId("validator-output")).toContainText(
    `unknown directive "bogus_directive" in /www/panel/nginx/sites/${DOMAIN}.conf:2`,
  )
  await expect(page.getByText("unsaved changes")).toBeVisible()

  // 4. The file on the server is what it was: a reload shows the original.
  await page.reload()
  await page.getByRole("tab", { name: "Config" }).click()
  await expect(page.locator(".cm-content")).toContainText(`includes/${DOMAIN}/names.conf;`)
  await expect(page.locator(".cm-content")).not.toContainText("bogus_directive")

  // 5. A valid edit that removes the names include saves, and detaches the
  //    Domains control: it is disabled and says which line is missing.
  const withoutNames = original
    .split("\n")
    .filter((line) => !line.includes(`includes/${DOMAIN}/names.conf;`))
    .join("\n")
  await setEditor(page, `${withoutNames}\n`)
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByText("Saved. nginx accepted the file and reloaded.")).toBeVisible()
  await expect(page.getByRole("cell", { name: `edit site file ${DOMAIN}` }).first()).toBeVisible()

  await page.getByRole("tab", { name: "Domain manager" }).click()
  await expect(page.locator('[data-feature="names"]').getByText("detached")).toBeVisible()
  await expect(page.getByLabel("Other domains")).toBeDisabled()
  await expect(page.getByText(/Switched off: the site file has no `include/)).toBeVisible()

  // 6. Regenerate, after a confirmation, connects it again.
  await page.getByRole("button", { name: "Regenerate site file" }).click()
  await page.getByRole("button", { name: "Regenerate", exact: true }).click()
  await expect(page.getByText("Site file regenerated.")).toBeVisible()
  await expect(page.locator('[data-feature="names"]').getByText("connected")).toBeVisible()
  await expect(page.getByLabel("Other domains")).toBeEnabled()

  // 7. The nginx page shows the service and saves a setting.
  await page.getByRole("link", { name: "Nginx" }).click()
  await expect(page.getByText(/nginx is running/)).toBeVisible()
  await page.getByLabel("Largest upload").fill("250m")
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByText("Saved. nginx reloaded.")).toBeVisible()

  // 8. Delete needs the domain typed, and returns to the list without the site.
  await page.getByRole("link", { name: "Sites" }).click()
  await page.getByRole("link", { name: DOMAIN, exact: true }).click()
  await page.getByRole("button", { name: "Delete site" }).click()
  const confirm = page.getByRole("button", { name: "Delete site" }).last()
  await expect(confirm).toBeDisabled()
  await page.getByLabel(`Type ${DOMAIN} to confirm`).fill(DOMAIN)
  await confirm.click()
  await expect(page).toHaveURL(/\/sites$/)
  await expect(page.getByText("No sites yet.")).toBeVisible()
})

/**
 * ONE dialog, both sections: a site that runs PHP and proxies /api.
 *
 * This is the merge the operator asked for. The PHP locations and the /api
 * proxy location end up in one site, and `location /` belongs to PHP exactly
 * once: the server refuses a "/" route next to PHP, so the dialog only offers
 * subpaths there. The check on the file goes through the Config tab, which
 * shows what nginx actually has.
 */
test("create a site with PHP and a /api route in the one dialog", async ({ page }) => {
  await login(page)

  const domain = "e2e-php-proxy.test"
  await page.getByRole("link", { name: "Sites" }).click()
  await expect(page).toHaveURL(/\/sites$/)
  await page.getByRole("button", { name: "Add site" }).click()
  await expect(page.getByRole("heading", { name: "Add a site" })).toBeVisible()

  await page.getByLabel("Domain", { exact: true }).fill(domain)

  // The PHP section: on, with the version the harness reports as ready.
  await page.getByRole("checkbox", { name: /Run PHP on this site/ }).check()
  await expect(page.getByLabel("PHP version")).toBeVisible()

  // The reverse-proxy section: one subpath row.
  await page.getByRole("button", { name: "Add route" }).click()
  await page.getByLabel("Path").fill("/api")
  await page.getByLabel("Pass requests to").fill("http://127.0.0.1:8080")

  await page.getByRole("button", { name: "Create site" }).click()
  await expect(page).toHaveURL(new RegExp(`/sites/${domain}$`))

  // Both parts are on the site: PHP in the overview, /api in the routes.
  await expect(page.getByText(/PHP 8\./)).toBeVisible()
  await expect(page.getByText("/api → http://127.0.0.1:8080")).toBeVisible()

  // The generated site file has the PHP block and the proxy include line.
  await page.getByRole("tab", { name: "Config" }).click()
  await expect(page.locator(".cm-content")).toContainText("# >>> pb:php")
  await expect(page.locator(".cm-content")).toContainText(`includes/${domain}/proxy.conf;`)
  const siteFile = await editorText(page)
  expect(siteFile).toContain("fastcgi_pass unix:/www/panel/run/php-")
  expect(siteFile).toContain("index index.php index.html index.htm;")

  // The route is editable after creation: rename it to /api/v2 and save.
  await page.getByRole("tab", { name: "Reverse proxy" }).click()
  await page.getByLabel("Path").fill("/api/v2")
  await page.getByRole("button", { name: "Save routes" }).click()
  await expect(page.getByText("Saved. nginx reloaded.")).toBeVisible()

  await page.getByRole("tab", { name: "Config" }).click()
  await page.getByRole("tab", { name: "Proxy" }).click()
  await expect(page.locator(".cm-content")).toContainText("location /api/v2 {")
  await expect(page.locator(".cm-content")).not.toContainText("location /api {")

  // Deleting it leaves the list empty again.
  await page.getByRole("tab", { name: "Reset or delete" }).click()
  await page.getByRole("button", { name: "Delete site" }).first().click()
  await page.getByLabel(`Type ${domain} to confirm`).fill(domain)
  await page.getByRole("button", { name: "Delete site" }).last().click()
  await expect(page).toHaveURL(/\/sites$/)
  await expect(page.getByText("No sites yet.")).toBeVisible()
})
