// LinkedIn's free job posting flow, as it stands: sign in, open the post-a-
// job page, fill the basics, the description, and the external apply link,
// then publish. LinkedIn changes this page without notice and challenges
// automation; every step names what it expected so a change is a clear
// error, never a silent wrong post.
import { requireEnv, expectVisible } from "./shared.js";

export async function post(page, posting, { env, dryRun, log }) {
  const email = requireEnv(env, "LINKEDIN_EMAIL");
  const password = requireEnv(env, "LINKEDIN_PASSWORD");

  log("signing in");
  await page.goto("https://www.linkedin.com/login");
  await page.locator("#username").fill(email);
  await page.locator("#password").fill(password);
  await page.getByRole("button", { name: /sign in/i }).click();
  if (await page.locator("input[name=pin], #input__email_verification_pin").first().isVisible({ timeout: 5_000 }).catch(() => false)) {
    throw new Error("LinkedIn asked for a verification code; sign in once by hand with JOBPOST_HEADFUL=1 and retry");
  }
  await expectVisible(page, page.locator("nav"), "the LinkedIn navigation after signing in");

  log("opening the job post flow");
  await page.goto("https://www.linkedin.com/job-posting/");
  await expectVisible(page, page.getByLabel(/job title/i), "the job title field on the post-a-job page");
  await page.getByLabel(/job title/i).fill(posting.title.split(" · ")[0]);
  const location = posting.title.includes(" · ") ? posting.title.split(" · ")[1] : "";
  if (location) {
    await page.getByLabel(/location/i).first().fill(location.replace(/,? ?(hybrid|on site)$/i, "").replace(/^Remote \((.*)\)$/, "$1"));
  }
  const workplace = /remote/i.test(location) ? "Remote" : /hybrid/i.test(location) ? "Hybrid" : "On-site";
  const workplaceField = page.getByLabel(/workplace type/i);
  if (await workplaceField.isVisible().catch(() => false)) await workplaceField.selectOption({ label: workplace });
  await page.getByRole("button", { name: /get started|continue|next/i }).first().click();

  log("writing the description");
  const description = page.locator("[contenteditable=true], textarea[name=description]").first();
  await expectVisible(page, description, "the description editor");
  await description.fill(posting.body);
  // Applicants come to the platform, not to LinkedIn's own apply.
  const external = page.getByLabel(/external website|receive applicants/i).first();
  if (await external.isVisible().catch(() => false)) {
    await external.check().catch(() => external.click());
  }
  const applyURL = page.getByLabel(/website address|apply url|application url/i).first();
  await expectVisible(page, applyURL, "the external apply URL field");
  await applyURL.fill(posting.apply_url);

  if (dryRun) {
    log("dry run: stopping before the final submit");
    return { url: page.url(), id: "", dry_run: true };
  }
  await page.getByRole("button", { name: /post job|publish/i }).first().click();
  // The confirmation carries the posting's own page.
  await page.waitForURL(/linkedin\.com\/jobs\/view\/(\d+)/, { timeout: 60_000 });
  const url = page.url();
  const id = url.match(/jobs\/view\/(\d+)/)?.[1] ?? "";
  return { url, id };
}
