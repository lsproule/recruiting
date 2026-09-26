import { Browser, BrowserContext, Page } from "@playwright/test";

import { addCandidate, asAdmin, createJob, json, move, must } from "../lib/api";
import { Fixture, readFixture } from "../lib/fixture";
import { waitForLink } from "../lib/mailbox";
import { recruiterStatePath } from "../lib/paths";
import { expect, test } from "../lib/test";

// Three candidates, two interviewers, twenty-second rounds: the whole sprint
// runs on the clock while the browsers watch it rotate.
test.describe.configure({ mode: "serial", timeout: 420_000 });

const ROUND = 20;
const BREAK = 5;

let fixture: Fixture;
let sprintId: string;
let jobId: string;
let sprintStageId: string;
let candidates: { name: string; email: string; applicationId: string; context: BrowserContext; page: Page }[] = [];
let vetter: BrowserContext;
let vetterPage: Page;
let recruiterPage: Page;

async function signIn(page: Page, email: string, password: string) {
  await page.goto("/app/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/app\//);
}

test.beforeAll(async ({ browser }: { browser: Browser }) => {
  fixture = readFixture();
  const api = await asAdmin(fixture);
  const job = await createJob(api, fixture, `E2E Sprint ${Date.now()}`, fixture.processes.fast_track);
  jobId = job.id;
  const stage = job.stages.find((s: { kind: string }) => s.kind === "sprint");
  if (!stage) throw new Error("the fast track has no sprint stage");
  sprintStageId = stage.id;
  const stamp = Date.now();
  for (const name of ["Ada Sprint", "Bo Sprint", "Cy Sprint"]) {
    const email = `e2e-${name.split(" ")[0].toLowerCase()}-${stamp}@example.test`;
    const app = await addCandidate(api, job.id, name, email);
    await move(api, app.id, stage.id);
    const context = await browser.newContext({ permissions: ["camera", "microphone"] });
    candidates.push({ name, email, applicationId: app.id, context, page: await context.newPage() });
  }
  vetter = await browser.newContext({ permissions: ["camera", "microphone"] });
  vetterPage = await vetter.newPage();
  await signIn(vetterPage, fixture.vetterEmail, fixture.vetterPassword);
  const recruiter = await browser.newContext({ storageState: recruiterStatePath });
  recruiterPage = await recruiter.newPage();
  await api.dispose();
});

test.afterAll(async () => {
  for (const c of candidates) await c.context.close();
  await vetter?.close();
  await recruiterPage?.context().close();
});

test("the recruiter plans a sprint from the stage and the plan says what it is", async () => {
  await recruiterPage.goto(`/app/pipeline/${jobId}`);
  await recruiterPage.getByRole("link", { name: "Set up sprint" }).click();
  await expect(recruiterPage.locator("h1")).toHaveText("New screening sprint");
  const plan = recruiterPage.locator(".sprint-plan");
  // Three candidates are ticked; the admin and the vetter get ticked here.
  for (const name of ["Vera Vetter", "e2e-admin@example.test"]) {
    await recruiterPage.locator(".option").filter({ hasText: name }).locator("input").check();
  }
  await expect(plan).toContainText("3 candidates × 2 interviewers");
  await expect(plan).toContainText("3 rounds");
});

test("the sprint is created and scheduled through the API with short rounds", async () => {
  const api = await asAdmin(fixture);
  const startsAt = new Date(Date.now() + 45_000).toISOString();
  const sprint = await json(
    "create the sprint",
    await api.post("/api/v1/sprints", {
      data: {
        job_id: jobId,
        stage_id: sprintStageId,
        name: "E2E speed screen",
        starts_at: startsAt,
        round_seconds: ROUND,
        break_seconds: BREAK,
        interviewer_ids: [fixture.vetterUserId, fixture.adminUserId],
        application_ids: candidates.map((c) => c.applicationId),
      },
    }),
  );
  sprintId = sprint.id;
  expect(sprint.rounds).toBe(3);
  expect(sprint.pairings).toHaveLength(6);
  const scheduled = await json("schedule the sprint", await api.post(`/api/v1/sprints/${sprintId}/schedule`));
  expect(scheduled.status).toBe("scheduled");
  await api.dispose();

  for (const c of candidates) {
    const link = await waitForLink(c.email, "screening sprint", /https?:\/\/\S+?\/sprint\/[A-Za-z0-9_-]+/);
    await c.page.goto(link);
    await expect(c.page.locator("h1")).toContainText(`Hi ${c.name}`);
    await expect(c.page.locator("#console-rounds li")).toHaveCount(2);
    await expect(c.page.locator("#console-stage")).toContainText("Your next conversation is with", { timeout: 20_000 });
  }
  await vetterPage.goto(`/app/sprints/${sprintId}/console`);
  await expect(vetterPage.locator("#console-rounds li")).toHaveCount(3);
  await expect(vetterPage.locator("#console-clock")).toContainText("Starts in", { timeout: 20_000 });
});

test("rooms rotate on the clock and every candidate meets the vetter once", async () => {
  const seen = new Map<string, Set<string>>();
  for (const c of candidates) seen.set(c.name, new Set());
  const deadline = Date.now() + (3 * ROUND + 2 * BREAK + 60) * 1000;
  let rounds = new Set<string>();
  while (Date.now() < deadline && rounds.size < 3) {
    const on = vetterPage.locator("#console-rounds li.on");
    if ((await on.count()) === 1) {
      const round = (await on.getAttribute("data-round")) ?? "";
      const who = (await on.locator(".console-round-who").innerText()).trim();
      if (!rounds.has(round)) {
        // The interviewer's frame shows this round's room, and the candidate's
        // page shows a frame for the very same pairing.
        const frame = vetterPage.locator("iframe.console-frame");
        await expect(frame).toHaveCount(1, { timeout: ROUND * 1000 });
        const pairing = await frame.getAttribute("data-pairing");
        const candidate = candidates.find((c) => c.name === who)!;
        await expect(candidate.page.locator("iframe.console-frame")).toHaveAttribute("data-pairing", pairing ?? "", { timeout: ROUND * 1000 });
        seen.get(who)!.add("Vera Vetter");
        rounds.add(round);
      }
    }
    await vetterPage.waitForTimeout(500);
  }
  expect([...rounds].sort()).toEqual(["0", "1", "2"]);
  for (const c of candidates) expect([...seen.get(c.name)!]).toEqual(["Vera Vetter"]);
});

test("the vetter rates each conversation from the console", async () => {
  await expect(vetterPage.locator("#console-clock")).toContainText("The sprint has ended", { timeout: 60_000 });
  const scores: Record<string, number> = { "Ada Sprint": 5, "Bo Sprint": 3, "Cy Sprint": 1 };
  for (const li of await vetterPage.locator("#console-rounds li").all()) {
    const who = (await li.locator(".console-round-who").innerText()).trim();
    await li.getByRole("link", { name: "rate" }).click();
    const card = vetterPage.locator(".rating-card");
    await expect(card).toContainText(`Rate ${who}`);
    // The radios are drawn as their labels: the inputs themselves have no size.
    await card.locator(`.rating-score:has(input[value="${scores[who]}"])`).click();
    await expect(card.locator(`input[name=score][value="${scores[who]}"]`)).toBeChecked();
    await card.locator(`.seg-opt:has(input[name=recommendation][value="${scores[who] >= 3 ? "yes" : "no"}"])`).click();
    await card.getByLabel("One line").fill(`${who} in twenty seconds`);
    await card.getByRole("button", { name: /Save rating|Update rating/ }).click();
    await expect(vetterPage.locator(".rating-card .flash-success")).toContainText("Saved");
  }
  for (const c of candidates) {
    await expect(c.page.locator("#console-stage")).toContainText("That was everyone", { timeout: 30_000 });
  }
});

test("the summary ranks the candidates and the recruiter advances the best", async () => {
  await recruiterPage.goto(`/app/sprints/${sprintId}`);
  const rows = recruiterPage.locator(".sprint-ranking tbody tr");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(0)).toContainText("Ada Sprint");
  await expect(rows.nth(0)).toContainText("5.0");
  await expect(rows.nth(2)).toContainText("Cy Sprint");
  await expect(rows.nth(0)).toContainText("Ada Sprint in twenty seconds");

  await rows.nth(0).getByRole("button", { name: "Advance" }).click();
  await expect(recruiterPage.locator(".detail-title .tag-accent")).toHaveText("Technical interview");

  const api = await asAdmin(fixture);
  const summary = await json("read the summary", await api.get(`/api/v1/sprints/${sprintId}/summary`));
  expect(summary.rows[0].candidate_name).toBe("Ada Sprint");
  expect(summary.rows[0].mean).toBe(5);
  expect(summary.sprint.phase).toBe("after");
  await api.dispose();
});
