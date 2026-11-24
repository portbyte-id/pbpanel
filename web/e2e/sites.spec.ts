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
  // Not bare "Password": the show/hide button's aria-label contains it too.
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
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

/**
 * One section of the site dialog's rail. It is a nav of buttons, not an ARIA
 * tablist: the dialog swaps a panel, it does not run a tab widget.
 */
function section(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Site sections" }).getByRole("button", { name })
}

test("create a site, break its config, detach a control, regenerate", async ({ page }) => {
  await login(page)

  // 1. Create a static site from the list page.
  await page.getByRole("link", { name: "Sites" }).click()
  await expect(page).toHaveURL(/\/sites$/)
  await page.getByRole("button", { name: "Add site" }).first().click()
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
  await section(page, "Config").click()
  await expect(page.locator(".cm-content")).toContainText("server {")
  const original = await editorText(page)
  expect(original).toContain("# >>> pb:names")

  await setEditor(page, "server {\n    bogus_directive on;\n}\n")
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByRole("alert")).toContainText("nginx rejected the configuration")
  await expect(page.getByTestId("validator-output")).toContainText(
    `unknown directive "bogus_directive" in /www/panel/nginx/sites/${DOMAIN}.conf:2`,
  )
  await expect(page.getByText("unsaved changes")).toBeVisible()

  // 4. The file on the server is what it was: a reload shows the original.
  await page.reload()
  await section(page, "Config").click()
  await expect(page.locator(".cm-content")).toContainText("# >>> pb:names")
  await expect(page.locator(".cm-content")).not.toContainText("bogus_directive")

  // 5. A valid edit that deletes the names block's marker lines saves, and
  //    detaches the Domains control: it is disabled and says what is missing.
  const withoutNames = original
    .split("\n")
    .filter((line) => line.trim() !== "# >>> pb:names" && line.trim() !== "# <<< pb:names")
    .join("\n")
  await setEditor(page, `${withoutNames}\n`)
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByText("Saved. nginx accepted the file and reloaded.")).toBeVisible()
  await expect(page.getByRole("cell", { name: `edit site file ${DOMAIN}` }).first()).toBeVisible()

  // The dialog re-reads the site: one reload, then the rail.
  await page.reload()
  await section(page, "Domain manager").click()
  await expect(page.locator('[data-feature="names"]').getByText("detached")).toBeVisible()
  await expect(page.getByLabel("Other domains")).toBeDisabled()
  await expect(page.getByText(/Switched off: the site file has no/)).toBeVisible()

  // 6. Regenerate, after a confirmation, connects it again.
  await section(page, "Reset or delete").click()
  await page.getByRole("button", { name: "Regenerate site file" }).click()
  await page.getByRole("button", { name: "Regenerate", exact: true }).click()
  await expect(page.getByText("Site file regenerated.")).toBeVisible()
  await page.reload()
  await section(page, "Domain manager").click()
  await expect(page.locator('[data-feature="names"]').getByText("connected")).toBeVisible()
  await expect(page.getByLabel("Other domains")).toBeEnabled()

  // 7. The nginx page shows the service and saves a setting. The site dialog is
  //    modal, so it is closed before the rail is used.
  await page.keyboard.press("Escape")
  await expect(page).toHaveURL(/\/sites$/)
  await page.getByRole("link", { name: "Runtimes" }).click()
  await expect(page.getByText(/nginx is running/)).toBeVisible()
  await page.getByRole("tab", { name: "Settings" }).click()
  await page.getByLabel("Largest upload").fill("250m")
  await page.getByRole("button", { name: "Save and reload" }).click()
  await expect(page.getByText("Saved. nginx reloaded.")).toBeVisible()

  // 8. Delete needs the domain typed, and returns to the list without the site.
  await page.getByRole("link", { name: "Sites" }).click()
  await page.getByRole("link", { name: DOMAIN, exact: true }).click()
  await section(page, "Reset or delete").click()
  await page.getByRole("button", { name: "Delete site" }).first().click()
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
  await page.getByRole("button", { name: "Add site" }).first().click()
  await expect(page.getByRole("heading", { name: "Add a site" })).toBeVisible()

  await page.getByLabel("Domain", { exact: true }).fill(domain)

  // The PHP section: on, with the version the harness reports as ready.
  await page.getByRole("checkbox", { name: /Run PHP on this site/ }).check()
  await expect(page.getByLabel("PHP version")).toBeVisible()

  // The reverse-proxy section: one subpath row.
  await page.getByRole("button", { name: "Add route" }).click()
  await page.getByRole("textbox", { name: "Path", exact: true }).fill("/api")
  await page.getByRole("textbox", { name: "Pass requests to" }).fill("http://127.0.0.1:8080")

  await page.getByRole("button", { name: "Create site" }).click()
  await expect(page).toHaveURL(new RegExp(`/sites/${domain}$`))

  // Both parts are on the site: PHP in the overview, /api in the routes.
  await expect(page.getByText(/PHP 8\./)).toBeVisible()
  await expect(page.getByText("/api → http://127.0.0.1:8080")).toBeVisible()

  // The generated site file has the PHP block and the proxy include line. The
  // editor virtualizes a long file (only the visible lines are in the DOM), so
  // the file is read from the server through this page's own session instead:
  // that is what nginx has, not a slice of what the editor rendered.
  await section(page, "Config").click()
  await expect(page.locator(".cm-content")).toContainText("index index.php")
  const siteFile = await (await page.request.get(`/api/sites/${domain}/file`)).json()
  expect(siteFile.content).toContain("# >>> pb:php")
  expect(siteFile.content).toContain(`include /www/panel/nginx/includes/${domain}/proxy.conf;`)
  expect(siteFile.content).toContain("fastcgi_pass unix:/www/panel/run/php-")
  expect(siteFile.content).toContain("index index.php index.html index.htm;")

  // The route is editable after creation: rename it to /api/v2 and save.
  await section(page, "Reverse proxy").click()
  await page.getByRole("textbox", { name: "Path", exact: true }).fill("/api/v2")
  await page.getByRole("button", { name: "Save routes" }).click()
  await expect(page.getByText("Saved. nginx reloaded.")).toBeVisible()

  const proxyConf = await (await page.request.get(`/api/sites/${domain}/includes/proxy.conf`)).json()
  expect(proxyConf.content).toContain("location /api/v2 {")
  expect(proxyConf.content).toContain("proxy_pass http://127.0.0.1:8080/;")
  expect(proxyConf.content).not.toContain("location /api {")

  // Deleting it leaves the list empty again.
  await section(page, "Reset or delete").click()
  await page.getByRole("button", { name: "Delete site" }).first().click()
  await page.getByLabel(`Type ${domain} to confirm`).fill(domain)
  await page.getByRole("button", { name: "Delete site" }).last().click()
  await expect(page).toHaveURL(/\/sites$/)
  await expect(page.getByText("No sites yet.")).toBeVisible()
})
