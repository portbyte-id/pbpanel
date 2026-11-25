import { expect, type Page, test } from "@playwright/test"

/**
 * The open-appsec journey: the Store's card, and what a site's Firewall tab says
 * when this host cannot run an engine, against the real embedded bundle.
 *
 * **What this host can prove, and what it cannot.** `scripts/e2e/server.go`
 * wires neither the firewall engine nor the appsec package, so both
 * `/api/appsec` and a site's `/waf` answer 503 here. Two honest states follow
 * from that and are asserted below: the Store says the engine is not part of
 * this build instead of offering an install, and the site's Firewall tab says
 * the firewall is unavailable instead of rendering a picker over nothing.
 *
 * The picker's own states — available, unavailable-with-the-API's-reason, the
 * verbatim tracking mark, the acknowledgement gate — are covered in the vitest
 * suites (`SiteWAF.test.tsx`, `StoreAppsecCard.test.tsx`), where the API's own
 * string can be compared character for character. Driving them in a browser
 * needs an e2e server with the firewall wired in, which is a Go change outside
 * this task's scope.
 */
const PASSWORD = "e2e-password-123"
const DOMAIN = "e2e-appsec.test"

async function login(page: Page) {
  await page.goto("/login")
  await page.getByLabel("Username").fill("admin")
  // Not bare "Password": the show/hide button's aria-label contains it too.
  await page.getByLabel("Password", { exact: true }).fill(PASSWORD)
  await page.getByRole("button", { name: "Sign in" }).click()
  await expect(page).toHaveURL(/\/dashboard/)
}

/** One section of the site dialog's rail. */
function section(page: Page, name: string) {
  return page.getByRole("navigation", { name: "Site sections" }).getByRole("button", { name })
}

test("the Store says what this build can install, and a site's firewall says why it cannot be set up", async ({
  page,
}) => {
  await login(page)

  // 1. The Store is reachable from the sidebar and holds the engine card.
  await page.getByRole("link", { name: "Store" }).click()
  await expect(page).toHaveURL(/\/store/)
  await expect(page.getByRole("heading", { name: "Firewall engines" })).toBeVisible()

  // On a build with no appsec package the card says so, and offers no install:
  // a button that cannot work is worse than a sentence that explains why.
  await expect(page.getByText(/This panel build has no open-appsec package/)).toBeVisible()
  await expect(page.getByRole("button", { name: /Install open-appsec/ })).toHaveCount(0)

  // 2. A site's Firewall tab reports that the firewall is unavailable here, with
  //    a retry, rather than rendering controls over a 503.
  await page.getByRole("link", { name: "Sites" }).click()
  await page.getByRole("button", { name: "Add site" }).first().click()
  await page.getByLabel("Domain", { exact: true }).fill(DOMAIN)
  await page.getByRole("button", { name: "Create site" }).click()
  await expect(page.getByRole("heading", { name: DOMAIN })).toBeVisible()

  await section(page, "Firewall").click()
  await expect(page.getByText("The firewall settings could not be loaded.")).toBeVisible()
  await expect(page.getByRole("button", { name: /Try again/ })).toBeVisible()
  // No engine is offered over a host that has none.
  await expect(page.getByRole("group", { name: "Inspection engine" })).toHaveCount(0)

  // 3. Delete the site again: the e2e server keeps one store for the whole run,
  //    and the sites journey asserts an empty list at its end.
  await section(page, "Reset or delete").click()
  await page.getByRole("button", { name: "Delete site" }).first().click()
  await page.getByLabel(`Type ${DOMAIN} to confirm`).fill(DOMAIN)
  await page.getByRole("button", { name: "Delete site" }).last().click()
  await expect(page.getByText("Add your first site")).toBeVisible()
})
