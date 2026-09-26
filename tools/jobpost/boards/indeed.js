// Indeed's employer post-a-job wizard: sign in, start a posting, fill the
// basics, description, and the "apply on your site" URL, then confirm.
import { requireEnv, expectVisible } from "./shared.js";

export async function post(page, posting, ctx) {
  return postOnIndeed(page, posting, ctx, "indeed");
}

export async function postOnIndeed(page, posting, { env, dryRun, log }, reportAs) {
  const email = requireEnv(env, "INDEED_EMAIL");
  const password = requireEnv(env, "INDEED_PASSWORD");

  log("signing in");
  await page.goto("https://secure.indeed.com/auth?hl=en&co=GB&service=employers");
  await page.getByLabel(/email/i).fill(email);
  await page.getByRole("button", { name: /continue/i }).click();
  await page.getByLabel(/password/i).fill(password);
  await page.getByRole("button", { name: /sign in/i }).click();
  if (await page.getByText(/verification code|verify it.s you/i).first().isVisible({ timeout: 5_000 }).catch(() => false)) {
    throw new Error("Indeed asked for a verification code; sign in once by hand with JOBPOST_HEADFUL=1 and retry");
  }

  log("starting a posting");
  await page.goto("https://employers.indeed.com/p/post-job");
  const title = page.getByLabel(/job title/i).first();
  await expectVisible(page, title, "the job title field of the post-a-job wizard");
  const [plainTitle, where = ""] = posting.title.split(" · ");
  await title.fill(plainTitle);
  const location = page.getByLabel(/job location|where/i).first();
  if (where && (await location.isVisible().catch(() => false))) {
    await location.fill(where.replace(/,? ?(hybrid|on site)$/i, "").replace(/^Remote \((.*)\)$/, "$1"));
  }
  await page.getByRole("button", { name: /continue|next|save and continue/i }).first().click();

  log("writing the description");
  const description = page.locator("[contenteditable=true], textarea").first();
  await expectVisible(page, description, "the description editor");
  await description.fill(posting.body);
  await page.getByRole("button", { name: /continue|next|save and continue/i }).first().click();

  log("pointing applicants at the platform");
  const external = page.getByLabel(/apply on (an )?external|your (own )?website|redirect/i).first();
  if (await external.isVisible().catch(() => false)) await external.check().catch(() => external.click());
  const applyURL = page.getByLabel(/website|url/i).first();
  await expectVisible(page, applyURL, "the external apply URL field");
  await applyURL.fill(posting.apply_url);

  if (dryRun) {
    log("dry run: stopping before the final confirm");
    return { url: page.url(), id: "", dry_run: true, board: reportAs };
  }
  await page.getByRole("button", { name: /confirm|post job|publish/i }).first().click();
  await page.waitForURL(/employers\.indeed\.com\/j\/|jk=([a-z0-9]+)/i, { timeout: 60_000 });
  const url = page.url();
  const id = url.match(/jk=([a-z0-9]+)/i)?.[1] ?? url.split("/").pop() ?? "";
  return { url, id, board: reportAs };
}
