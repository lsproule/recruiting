import { recruiterStatePath } from "../lib/paths";
import { expect, test } from "../lib/test";

test.use({ storageState: recruiterStatePath });

// The library is read, a process is written, and a job is built from it, in
// that order.
test.describe.configure({ mode: "serial" });

let processURL: string;

test("the org starts with the three built-in processes", async ({ page }) => {
  await page.goto("/app/processes");
  await expect(page.locator("h1")).toHaveText("How this org hires");
  const cards = page.locator(".process-grid").first().locator(".process-card");
  await expect(cards).toHaveCount(3);
  await expect(cards.filter({ has: page.locator(".tag-accent", { hasText: "Default" }) })).toHaveCount(1);
  await expect(cards.filter({ hasText: "Agency standard" })).toContainText("Default");
  const loop = cards.filter({ hasText: "Engineering loop" });
  await expect(loop).toContainText("Recruiter call");
  await expect(loop).toContainText("phone · 20 min");
  await expect(loop).toContainText("Screening sprint");
  await expect(loop).toContainText("5 min rounds");
  await expect(loop).toContainText("Technical interview");
  await expect(loop).toContainText("video · 60 min");
  await expect(loop).toContainText("HR interview");
});

test("a new process gets a sprint stage and a video interview with their settings", async ({ page }) => {
  await page.goto("/app/processes");
  const blank = page.locator(".process-card-blank");
  await blank.getByLabel("Name").fill("Contract hire");
  await blank.getByLabel("What it is for").fill("Short engagements");
  await blank.getByRole("button", { name: "Create process" }).click();
  await expect(page).toHaveURL(/\/app\/processes\/[0-9a-f-]{36}$/);
  processURL = page.url();
  await expect(page.locator("h1")).toContainText("Contract hire");
  await expect(page.locator(".stage-chip")).toHaveCount(3);

  // A sprint stage with four-minute rounds.
  const add = page.locator(".stage-card-new");
  await add.getByLabel("New stage name").fill("Speed screen");
  await add.getByLabel("New stage kind").selectOption("sprint");
  await expect(add.getByLabel("Round length (minutes)")).toBeVisible();
  await add.getByLabel("Round length (minutes)").fill("4");
  await add.getByLabel("Break between rounds (seconds)").fill("30");
  await add.getByRole("button", { name: "Add stage" }).click();
  await expect(page.locator(".stage-chip")).toHaveCount(4);
  await expect(page.locator(".stage-chip").nth(1)).toContainText("Speed screen");
  await expect(page.locator(".stage-chip").nth(1)).toContainText("4 min rounds · 30s break");

  // A video interview of 45 minutes.
  await add.getByLabel("New stage name").fill("Technical");
  await add.getByLabel("New stage kind").selectOption("interview");
  await add.getByLabel("Format").selectOption("video");
  await add.getByLabel("Length (minutes)", { exact: true }).fill("45");
  await add.getByRole("button", { name: "Add stage" }).click();
  await expect(page.locator(".stage-chip")).toHaveCount(5);
  await expect(page.locator(".stage-chip").nth(2)).toContainText("video · 45 min");

  // A round too short is refused with the reason, and nothing changes.
  // A card's name is an input's value, not text, so the card is found by it.
  const sprintCard = page.locator('.stage-card:has(input[name=name][value="Speed screen"])');
  // The browser's own minimum matches the server's, so it is switched off
  // here to show the server refuses the value on its own.
  await sprintCard.evaluate((form) => ((form as HTMLFormElement).noValidate = true));
  await sprintCard.getByLabel("Round length (minutes)").fill("0.1");
  await sprintCard.getByRole("button", { name: "Save stage" }).click();
  await expect(page.locator(".stage-editor .flash-error")).toContainText("rounds must last");
  await expect(page.locator(".stage-chip").nth(1)).toContainText("4 min rounds");
});

test("a job built from it carries the stages and their settings", async ({ page }) => {
  await page.goto(processURL);
  await page.getByRole("button", { name: "Make default" }).first().click();
  await expect(page).toHaveURL(/\/app\/processes$/);
  await expect(page.locator(".process-card").filter({ hasText: "Contract hire" }).locator(".tag-accent")).toHaveText("Default");

  await page.goto("/app/jobs/new");
  await page.getByLabel("Client company").selectOption({ label: "Acme E2E" });
  await page.getByLabel("Title").fill("Contract Go engineer");
  await page.getByRole("button", { name: "Create job" }).click();
  await expect(page).toHaveURL(/\/app\/jobs\/[0-9a-f-]{36}\/pipeline$/);
  const chips = page.locator(".stage-chip");
  await expect(chips).toHaveCount(5);
  await expect(chips.nth(1)).toContainText("Speed screen");
  await expect(chips.nth(1)).toContainText("4 min rounds · 30s break");
  await expect(chips.nth(2)).toContainText("Technical");
  await expect(chips.nth(2)).toContainText("video · 45 min");
  await expect(page.locator('.stage-card:has(input[name=name][value="Technical"])').getByLabel("Default interviewer")).toBeVisible();

  // The org's default goes back to the standard process for the other scenarios.
  await page.goto("/app/processes");
  await page
    .locator(".process-card")
    .filter({ hasText: "Agency standard" })
    .getByRole("button", { name: "Make default" })
    .click();
  await expect(page.locator(".process-card").filter({ hasText: "Agency standard" }).first().locator(".tag-accent")).toHaveText("Default");
});
