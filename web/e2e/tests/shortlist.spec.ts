import { Page } from "@playwright/test";

import { recruiterStatePath } from "../lib/paths";
import { expect, test } from "../lib/test";

// The packet is built once and then read as the client, in that order.
test.describe.configure({ mode: "serial", timeout: 300_000 });

let shortlistURL: string;

async function signInAsClient(page: Page, email: string, password: string) {
  await page.goto("/client/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/client\//);
}

test.describe("the recruiter sends a packet", () => {
  test.use({ storageState: recruiterStatePath });

  test("builds a shortlist from the scored pool and sends it", async ({ page, fixture }) => {
    const builder = `/app/jobs/${fixture.jobId}/shortlist`;
    // Scoring is a queued job, so the pool fills a moment after the sitting.
    await expect
      .poll(
        async () => {
          await page.goto(builder);
          return page.locator(".shortlist-pool").innerText();
        },
        { timeout: 180_000, intervals: [2000] },
      )
      .toContain("E2E Candidate");

    await page.locator(".shortlist-row").first().getByRole("button", { name: "Add" }).click();
    await expect(page.locator(".shortlist-pick")).toHaveCount(1);
    await page.locator("#shortlist-note").fill("Strongest sitting on the job.");
    await page.getByRole("button", { name: "Send to client" }).click();

    await expect(page.locator(".shortlist-history")).toContainText("E2E Candidate");
  });
});

test.describe("the client reads it", () => {
  test("sees the ranked packet, replays the sitting, and sees no integrity signals", async ({
    page,
    fixture,
  }) => {
    await signInAsClient(page, fixture.clientEmail, fixture.clientPassword);

    await page.goto(`/client/jobs/${fixture.jobId}`);
    await page.getByRole("link", { name: "Read the shortlist your recruiter sent" }).click();
    shortlistURL = page.url();
    await expect(page.locator("h1")).toHaveText("Shortlist");
    await expect(page.locator(".shortlist-note")).toContainText("Strongest sitting on the job.");

    const picks = page.locator(".shortlist-ranked-pick");
    await expect(picks).toHaveCount(1);
    await picks.first().getByRole("link").click();

    // The replay island boots read-only: it plays the editing, and asks for
    // no webcam frames.
    await expect(page.locator("#replay .replay-play")).toBeVisible();
    const config = await page
      .locator("#replay-config")
      .evaluate((el) => JSON.parse(el.textContent ?? "{}"));
    expect(config.readonly).toBe(true);
    expect(config.snapshots_url).toBeUndefined();
    await expect(page.locator("#replay .replay-legend")).toContainText("each run and each submit");

    await page.locator("#replay .replay-play").click();
    await expect(page.locator("#replay .replay-play")).toHaveText("Pause");

    const body = await page.locator("body").innerText();
    for (const forbidden of ["Integrity", "integrity", "risk score", "snapshot", "webcam", "Paste"]) {
      expect(body).not.toContain(forbidden);
    }
    await expect(page.locator(".replay-marker-snapshot")).toHaveCount(0);
    await expect(page.locator(".replay-marker-paste")).toHaveCount(0);
  });

  test("cannot reach the packet without signing in", async ({ browser }) => {
    const anon = await browser.newContext();
    const res = await anon.request.get(shortlistURL, { maxRedirects: 0 });
    expect(res.status()).toBe(303);
    expect(res.headers()["location"]).toContain("/client/login");
    await anon.close();
  });
});
