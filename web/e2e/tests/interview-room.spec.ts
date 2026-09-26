import { Browser, BrowserContext, Page } from "@playwright/test";

import { addCandidate, asAdmin, createJob, json, move, must } from "../lib/api";
import { sql } from "../lib/db";
import { Fixture, readFixture } from "../lib/fixture";
import { waitForLink } from "../lib/mailbox";
import { expect, test } from "../lib/test";

// One video interview from booking to scorecard: the candidate books, both
// join, they see each other, a screen is shared, the editor is shared, code
// runs, and what was written stays with the application.
test.describe.configure({ mode: "serial", timeout: 300_000 });

let fixture: Fixture;
let candidate: BrowserContext;
let interviewer: BrowserContext;
let candidatePage: Page;
let interviewerPage: Page;
let applicationId: string;
let stageId: string;
let bookingURL: string;

// getDisplayMedia has no synthetic source the way the camera does, so the
// candidate's browser hands out a painted canvas as "the screen".
const fakeScreen = `
  navigator.mediaDevices.getDisplayMedia = async () => {
    const canvas = document.createElement("canvas");
    canvas.width = 320; canvas.height = 180;
    const ctx = canvas.getContext("2d");
    let hue = 0;
    setInterval(() => { hue = (hue + 7) % 360; ctx.fillStyle = "hsl(" + hue + " 70% 50%)"; ctx.fillRect(0, 0, 320, 180); }, 100);
    return canvas.captureStream(10);
  };
`;

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
  const job = await createJob(api, fixture, `E2E Video ${Date.now()}`);
  const stage = job.stages.find((s: { kind: string }) => s.kind === "interview");
  stageId = stage.id;
  await must(
    "make the interview a video room with the vetter as its default interviewer",
    await api.put(`/api/v1/jobs/${job.id}/stages/${stage.id}`, {
      data: { name: stage.name, kind: "interview", interview_format: "video", duration_minutes: 30 },
    }),
  );
  const email = `e2e-room-${Date.now()}@example.test`;
  const app = await addCandidate(api, job.id, "Rowan Room", email);
  applicationId = app.id;
  await must(
    "assign the vetter",
    await api.put(`/api/v1/applications/${app.id}/vetter`, { data: { vetter_id: fixture.vetterUserId } }),
  );
  await move(api, app.id, stage.id);
  bookingURL = await waitForLink(email, "interview", /https?:\/\/\S+?\/book\/[A-Za-z0-9_-]+/);
  await api.dispose();

  candidate = await browser.newContext({ permissions: ["camera", "microphone"] });
  await candidate.addInitScript(fakeScreen);
  candidatePage = await candidate.newPage();
  interviewer = await browser.newContext({ permissions: ["camera", "microphone"] });
  interviewerPage = await interviewer.newPage();
  await signIn(interviewerPage, fixture.vetterEmail, fixture.vetterPassword);
});

test.afterAll(async () => {
  await candidate?.close();
  await interviewer?.close();
});

test("the candidate books a slot and is told the interview is by video", async () => {
  await candidatePage.goto(bookingURL);
  await expect(candidatePage.locator("h1")).toBeVisible();
  await candidatePage.locator("button.pick").first().click();
  await candidatePage.getByRole("button", { name: /^Confirm/ }).click();
  await expect(candidatePage.locator(".flash-success")).toContainText("Booked for");
  await expect(candidatePage.locator(".current")).toContainText("This interview is by video");
  await expect(candidatePage.locator(".current")).toContainText("opens here ten minutes before");
});

test("the room opens ten minutes before the slot for both sides", async () => {
  // A booking is at least two hours out; the afternoon passes.
  sql(`update interview_slot set starts_at = now() + interval '5 minutes', ends_at = now() + interval '35 minutes' where application_id = '${applicationId}'`);

  await candidatePage.reload();
  await candidatePage.getByRole("link", { name: "Join your interview" }).click();
  await expect(candidatePage.locator("#room")).toHaveAttribute("data-state", "joined", { timeout: 30_000 });
  await expect(candidatePage.locator(".room-tile-self video")).toBeVisible();

  await interviewerPage.goto("/app/interviews");
  const row = interviewerPage.locator(".interview-row").filter({ hasText: "Rowan Room" });
  await expect(row).toContainText("Video");
  await row.getByRole("link", { name: "Join room" }).click();
  await expect(interviewerPage.locator("#room")).toHaveAttribute("data-state", "joined", { timeout: 30_000 });
});

test("each side sees the other's camera", async () => {
  for (const [page, who] of [
    [interviewerPage, "Rowan Room"],
    [candidatePage, "Vera Vetter"],
  ] as const) {
    const remote = page.locator(".room-tile-camera").filter({ hasText: who });
    await expect(remote).toHaveCount(1);
    await expect
      .poll(() => remote.locator("video").evaluate((v: HTMLVideoElement) => v.videoWidth * v.videoHeight), { timeout: 60_000 })
      .toBeGreaterThan(0);
  }
});

test("a shared screen shows up as its own tile on the other side", async () => {
  await candidatePage.getByRole("button", { name: "Share screen" }).click();
  await expect(candidatePage.getByRole("button", { name: "Stop sharing" })).toBeVisible();
  const screen = interviewerPage.locator(".room-tile-screen");
  await expect(screen).toHaveCount(1, { timeout: 30_000 });
  await expect(screen).toContainText("Rowan Room's screen");
  await expect
    .poll(() => screen.locator("video").evaluate((v: HTMLVideoElement) => v.videoWidth), { timeout: 60_000 })
    .toBeGreaterThan(0);
  await candidatePage.getByRole("button", { name: "Stop sharing" }).click();
  await expect(interviewerPage.locator(".room-tile-screen")).toHaveCount(0, { timeout: 30_000 });
});

test("the editor is shared and runs python for whoever presses Run", async () => {
  const editor = (page: Page) => page.locator("#room .assess-editor .cm-content");
  await editor(interviewerPage).click();
  await interviewerPage.keyboard.press("ControlOrMeta+a");
  await interviewerPage.keyboard.press("Backspace");
  await interviewerPage.keyboard.insertText('print("hello from the room")');
  await expect(editor(candidatePage)).toContainText('print("hello from the room")', { timeout: 30_000 });

  await candidatePage.locator("#room select[aria-label=Language]").selectOption("python");
  await candidatePage.getByRole("button", { name: "Run", exact: true }).click();
  await expect(candidatePage.locator("#room .room-stdout")).toContainText("hello from the room", { timeout: 240_000 });
  // The language change reached the interviewer's page too.
  await expect(interviewerPage.locator("#room select[aria-label=Language]")).toHaveValue("python");
  await expect(interviewerPage.locator("#room .room-saved")).toContainText("Saved", { timeout: 30_000 });
});

test("the scorecard is filed and the application keeps the code", async () => {
  await interviewerPage.goto(`/app/applications/${applicationId}/scorecard/${stageId}`);
  await interviewerPage.getByLabel("Overall").selectOption("yes");
  await interviewerPage.locator("textarea[name=notes]").fill("Solid pairing session.");
  await interviewerPage.getByRole("button", { name: /Submit scorecard|Update scorecard/ }).click();

  await interviewerPage.goto(`/app/applications/${applicationId}`);
  const panel = interviewerPage.locator(".interview-panel");
  await expect(panel).toContainText("Vera Vetter");
  await expect(panel).toContainText("Written in the interview");
  await expect(panel.locator(".interview-code")).toContainText('print("hello from the room")');
  await expect(panel).toContainText("Scorecard filed");

  const api = await asAdmin(fixture);
  const slotId = sql(`select id from interview_slot where application_id = '${applicationId}' limit 1`);
  const code = await json("read the room code", await api.get(`/api/v1/rooms/slot/${slotId}/code`));
  expect(code.language).toBe("python");
  expect(code.source).toContain("hello from the room");
  await api.dispose();
});
