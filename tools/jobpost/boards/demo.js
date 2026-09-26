// The local demo board (fake-board.js), driven through the browser like a
// real one: open the form, fill it, submit, read the posting's URL back.
export async function post(page, posting, { env, dryRun, log }) {
  const base = (env.JOBPOST_DEMO_BOARD_URL ?? "http://localhost:8765").replace(/\/$/, "");
  log(`opening ${base}/post`);
  await page.goto(`${base}/post`);
  await page.getByLabel("Title").fill(posting.title);
  await page.getByLabel("Description").fill(posting.body);
  await page.getByLabel("Apply URL").fill(posting.apply_url);
  if (dryRun) {
    log("dry run: form filled, not submitted");
    return { url: `${base}/post`, id: "", dry_run: true };
  }
  await page.getByRole("button", { name: "Publish posting" }).click();
  await page.waitForURL(/\/jobs\/\d+$/);
  const url = page.url();
  return { url, id: url.slice(url.lastIndexOf("/") + 1) };
}
