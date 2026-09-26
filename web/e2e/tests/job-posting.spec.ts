import { expect, test } from "../lib/test";
import { recruiterStatePath } from "../lib/paths";

test.use({ storageState: recruiterStatePath });

// Posting a job runs the whole path: the platform writes the ad from the
// job, the worker's browser places it on the (local demo) board, and the
// panel shows where it landed. The board's page then carries the apply link
// back to the platform.
test("a job is posted to the demo board through the browser tool", async ({ page, fixture, request }) => {
  await page.goto(`/app/jobs/${fixture.jobId}/postings`);
  await expect(page.getByRole("heading", { name: "Postings" })).toBeVisible();

  // The copy is written from the job's own fields and previewed before it goes.
  const demo = page.locator(".board", { hasText: "Demo board (local)" });
  await demo.locator("summary").click();
  await expect(demo.locator(".posting-copy")).toContainText("is hiring a");
  await expect(demo.locator(".posting-copy")).toContainText(`/apply/${fixture.orgSlug}/`);
  await demo.getByRole("button", { name: "Post to Demo board (local)" }).click();
  await expect(page.locator(".flash-success")).toContainText("Queued for Demo board (local)");

  // The worker drives a real browser through the board's form; the row
  // turns live with the board's own URL for the posting. The post redirected
  // to the panel, so a plain visit reads the current state without posting
  // again.
  await expect
    .poll(
      async () => {
        await page.goto(`/app/jobs/${fixture.jobId}/postings`);
        return page.locator(".posting-posted").count();
      },
      { timeout: 120_000, intervals: [2_000] },
    )
    .toBe(1);
  const where = await page.locator(".posting-posted a").first().getAttribute("href");
  expect(where).toMatch(/\/jobs\/\d+$/);

  // The board's page shows the ad with the apply link pointing back here.
  const res = await request.get(where!);
  expect(res.ok()).toBeTruthy();
  const html = await res.text();
  expect(html).toContain("is hiring a");
  expect(html).toContain(`/apply/${fixture.orgSlug}/`);

  // The same record is on the API.
  const api = await request.get(`/api/v1/jobs/${fixture.jobId}/postings`, {
    headers: { Authorization: `Bearer ${fixture.apiToken}` },
  });
  expect(api.ok()).toBeTruthy();
  const body = (await api.json()) as { postings: { board: string; status: string; external_url: string }[] };
  expect(body.postings).toHaveLength(1);
  expect(body.postings[0]).toMatchObject({ board: "demo", status: "posted", external_url: where });
});
