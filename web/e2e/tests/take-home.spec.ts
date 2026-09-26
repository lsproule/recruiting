import { addCandidate, asAdmin, createJob, json, move, must } from "../lib/api";
import { waitForLink } from "../lib/mailbox";
import { expect, test } from "../lib/test";

// A take-home is the same set with days rather than minutes: the candidate
// reads a deadline, not a countdown, and can leave and come back.
test.describe.configure({ timeout: 180_000 });

test("a take-home shows its window, then a due date, and survives leaving", async ({ browser, fixture }) => {
  const api = await asAdmin(fixture);
  const job = await createJob(api, fixture, `E2E Take-home ${Date.now()}`);
  const stage = job.stages.find((s: { kind: string }) => s.kind === "assessment");
  const assessment = await json(
    "create a take-home",
    await api.post("/api/v1/assessments", {
      data: {
        name: "E2E take-home",
        format: "take_home",
        duration_minutes: 2 * 24 * 60 + 6 * 60,
        invite_window_days: 7,
        problem_ids: [fixture.problemId],
      },
    }),
  );
  expect(assessment.format).toBe("take_home");
  expect(assessment.integrity.webcam).toBeFalsy();
  await must(
    "attach it",
    await api.put(`/api/v1/jobs/${job.id}/stages/${stage.id}/assessment`, { data: { assessment_id: assessment.id } }),
  );
  const email = `e2e-takehome-${Date.now()}@example.test`;
  const app = await addCandidate(api, job.id, "Tess Takehome", email);
  await move(api, app.id, stage.id);
  const link = await waitForLink(email, "assessment", /https?:\/\/\S+?\/assess\/[A-Za-z0-9_-]+/);
  await api.dispose();

  const context = await browser.newContext();
  const page = await context.newPage();
  await page.goto(link);
  await expect(page.locator(".assess-intro")).toContainText("This is a take-home");
  await expect(page.locator(".assess-intro")).toContainText("2 days and 6 hours");
  await page.getByRole("button", { name: "Start the take-home" }).click();
  await expect(page.locator("#assess .assess-editor .cm-content")).toBeVisible();
  await expect(page.locator(".assess-timer")).toContainText("Due");
  await expect(page.locator(".assess-timer")).not.toContainText("Time remaining");
  await expect(page.locator("#assess-consent-form")).toHaveCount(0);

  // Leave, and come back through the same link: the sitting is still open.
  await page.goto("about:blank");
  await page.goto(link);
  await expect(page).toHaveURL(/\/assess\/$/);
  await expect(page.locator("#assess .assess-editor .cm-content")).toBeVisible();
  await expect(page.locator(".assess-timer")).toContainText("Due");
  await context.close();
});
