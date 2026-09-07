import { Browser, Page, Request } from "@playwright/test";

import { Fixture, readFixture } from "../lib/fixture";
import { chooseKeymap, chooseLanguage, editorText, replaceSource, results } from "../lib/editor";
import { recruiterStatePath } from "../lib/paths";
import { referenceSolution } from "../lib/seed";
import { expect, test } from "../lib/test";

// One sitting runs through the whole file: consent, the keymaps, fullscreen,
// a webcam snapshot, and finally a submission the shortlist scenario needs.
test.describe.configure({ mode: "serial", timeout: 300_000 });

let fixture: Fixture;
let page: Page;
let attemptID: string;
/** Armed before the sitting starts, so an early frame is not missed. */
let firstSnapshot: Promise<{ status(): number }>;

/** posted collects the island's writes so a later test can assert on them. */
const posted: { url: string; status: number; body: string }[] = [];

function postsMatching(pattern: RegExp) {
  return posted.filter((p) => pattern.test(p.url));
}

test.beforeAll(async ({ browser }: { browser: Browser }) => {
  fixture = readFixture();
  const context = await browser.newContext({ permissions: ["camera"] });
  page = await context.newPage();
  page.on("requestfinished", async (req: Request) => {
    if (req.method() !== "POST") return;
    const res = await req.response();
    posted.push({
      url: req.url(),
      status: res ? res.status() : 0,
      body: req.postData() ?? "",
    });
  });
  firstSnapshot = page.waitForResponse((r) => /\/snapshots$/.test(r.url()), { timeout: 120_000 });
  // The invite link swaps itself for the sealed cookie and lands on /assess/.
  await page.goto(fixture.assessURL);
  await expect(page).toHaveURL(/\/assess\/$/);
});

test.afterAll(async () => {
  await page?.context().close();
});

test("consent turns the camera on, stores a photo of an ID, and starts the sitting", async () => {
  await expect(page.locator("#assess-consent-form")).toBeVisible();
  await expect(page.locator(".consent-ledger")).toContainText("camera");

  const start = page.locator("#assess-consent-start");
  await expect(start).toBeDisabled();

  await page.getByRole("button", { name: "Turn on the camera" }).click();
  await expect(page.locator("video.consent-preview")).toBeVisible();
  await expect(page.locator("#assess-consent-status")).toContainText("photo ID");

  await page.getByRole("button", { name: "Take the ID photo" }).click();
  await expect(page.locator("img.consent-shot")).toBeVisible();
  await expect(page.locator("#assess-consent-status")).toContainText("will be stored");

  await page.locator("#assess-consent-agree").check();
  await expect(start).toBeEnabled();

  const identity = page.waitForResponse((r) => /\/attempts\/[0-9a-f-]+\/identity$/.test(r.url()));
  await start.click();
  expect((await identity).status()).toBeLessThan(300);

  // Agreeing starts the sitting, so the page comes back with the editor.
  await expect(page.locator("#assess .assess-editor .cm-content")).toBeVisible();
  attemptID = await page.locator("#assess-config").evaluate((el) => JSON.parse(el.textContent ?? "{}").attempt_id);
  expect(attemptID).toMatch(/^[0-9a-f-]{36}$/);
});

test("vim dd deletes a line and emacs C-k kills to the end of one", async () => {
  await replaceSource(page, "alpha\nbravo\ncharlie\n");

  await chooseKeymap(page, "vim");
  await expect(page.locator("#assess .cm-vim-panel")).toBeVisible();
  await page.locator("#assess .cm-content").click();
  await page.keyboard.press("Escape");
  await page.keyboard.type("gg");
  await page.keyboard.type("dd");
  await expect.poll(() => editorText(page)).not.toContain("alpha");
  expect(await editorText(page)).toContain("bravo");

  await chooseKeymap(page, "emacs");
  await page.locator("#assess .cm-content").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Control+k");
  await expect.poll(() => editorText(page)).not.toContain("bravo");
  expect(await editorText(page)).toContain("charlie");
});

test("leaving fullscreen is recorded", async () => {
  const alert = page.locator("#assess .assess-alert");
  const entered = await page.evaluate(async () => {
    // The island asks for fullscreen on load, before any gesture; the retry
    // button exists precisely because the browser can refuse that.
    if (document.fullscreenElement) return true;
    const button = document.querySelector<HTMLButtonElement>(".assess-alert-action");
    button?.click();
    await new Promise((r) => setTimeout(r, 500));
    return document.fullscreenElement !== null;
  });
  if (entered) {
    await expect(alert).toBeHidden();
  }
  await page.evaluate(() => document.exitFullscreen?.().catch(() => undefined));
  if (entered) {
    await expect(alert).toContainText("You left fullscreen");
  }
  // Whether or not the browser granted fullscreen, the exit reaches the
  // recording; the manifest check at the end of the file is what proves it.
});

test("the mocked camera posts a webcam snapshot", async () => {
  // The first frame is due a few seconds in, jittered, and the server is what
  // turns an accepted frame into the recording's snapshot event.
  expect((await firstSnapshot).status()).toBeLessThan(300);
});

test("the sitting is submitted and scored", async () => {
  await chooseKeymap(page, "default");
  await chooseLanguage(page, "python");
  await replaceSource(page, referenceSolution(fixture.problemTitle, "python"));
  await page.getByRole("button", { name: "Submit", exact: true }).click();
  await expect(results(page).locator("p").first()).toContainText("Submission done", { timeout: 240_000 });

  page.once("dialog", (d) => d.accept());
  await page.getByRole("button", { name: "Finish assessment" }).click();
  await expect(page.locator(".assess-banner, .assess-intro")).toContainText(/submitted/i, {
    timeout: 60_000,
  });
});

test("the recording carries the keymap, fullscreen, and snapshot events", async () => {
  // A webcam frame is due every 15s with up to 10s of jitter, so at least one
  // has been posted by the time the sitting above finished.
  expect(postsMatching(/\/snapshots$/).length).toBeGreaterThan(0);
  for (const post of postsMatching(/\/snapshots$/)) expect(post.status).toBeLessThan(300);
  for (const post of postsMatching(/\/identity$/)) expect(post.status).toBeLessThan(300);

  // The manifest authenticates as a signed-in org user, so it is read with
  // the recruiter's own session rather than the candidate's sealed cookie.
  const recruiter = await page.context().browser()!.newContext({ storageState: recruiterStatePath });
  const kinds: Record<string, unknown[]> = {};
  for (let after = 0; ; ) {
    const res = await recruiter.request.get(
      `${fixture.baseURL}/api/v1/replay/${attemptID}${after ? `?after_seq=${after}` : ""}`,
    );
    expect(res.status(), await res.text()).toBe(200);
    const body = (await res.json()) as {
      events: { kind: string; payload: unknown }[];
      next_after_seq: number;
    };
    for (const ev of body.events) (kinds[ev.kind] ??= []).push(ev.payload);
    if (!body.next_after_seq) break;
    after = body.next_after_seq;
  }
  await recruiter.close();

  expect(Object.keys(kinds)).toEqual(expect.arrayContaining(["keymap", "fullscreen_exit", "snapshot"]));
  expect(JSON.stringify(kinds.keymap)).toContain("vim");
  expect(JSON.stringify(kinds.keymap)).toContain("emacs");
});
