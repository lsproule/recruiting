import { Browser, BrowserContext, Page } from "@playwright/test";

import { request } from "@playwright/test";

import { asAdmin, createJob, json, must } from "../lib/api";
import { Fixture, readFixture } from "../lib/fixture";
import { waitForLink } from "../lib/mailbox";
import { recruiterStatePath } from "../lib/paths";
import { expect, test } from "../lib/test";

// A person joins the network from the public page, a company describes who
// it wants through the API and asks to meet the anonymised match, the
// recruiter sends the opportunity, the person says yes, and the company
// reads the new candidate on its portal and in its change feed.
test.describe.configure({ mode: "serial", timeout: 240_000 });

let fixture: Fixture;
let jobId: string;
let requestId: string;
let matchId: string;
let introId: string;
let clientToken: string;
let personEmail: string;
let person: BrowserContext;
let personPage: Page;
let recruiterPage: Page;
let clientPage: Page;

async function signInAsClient(page: Page, email: string, password: string) {
  await page.goto("/client/login");
  await page.getByLabel("Email").fill(email);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/client\//);
}

test.beforeAll(async ({ browser }: { browser: Browser }) => {
  fixture = readFixture();
  const api = await asAdmin(fixture);
  const job = await createJob(api, fixture, `E2E Platform ${Date.now()}`);
  jobId = job.id;
  await api.dispose();
  personEmail = `e2e-grace-${Date.now()}@example.test`;
  person = await browser.newContext();
  personPage = await person.newPage();
  recruiterPage = await (await browser.newContext({ storageState: recruiterStatePath })).newPage();
  clientPage = await (await browser.newContext()).newPage();
  await signInAsClient(clientPage, fixture.clientEmail, fixture.clientPassword);
});

test.afterAll(async () => {
  await person?.close();
  await recruiterPage?.context().close();
  await clientPage?.context().close();
});

test("a person joins the network from the public page and gets their profile link", async () => {
  await personPage.goto(`/talent/${fixture.orgSlug}`);
  await expect(personPage.locator("h1")).toHaveText("Join the talent network");
  await personPage.getByLabel("Your name").fill("Grace Hopper");
  await personPage.getByLabel("Email", { exact: true }).fill(personEmail);
  await personPage.getByLabel("One line about you").fill("Compiler engineer who runs clusters");
  await personPage.getByLabel("Skills", { exact: true }).fill("go, postgres, kubernetes");
  await personPage.getByLabel("Seniority").selectOption("senior");
  await personPage.getByLabel("Location", { exact: true }).fill("Berlin");
  await personPage.getByLabel("Remote").selectOption("remote");
  await personPage.getByLabel(/may keep this profile/).check();
  await personPage.getByRole("button", { name: "Join the network" }).click();
  await expect(personPage.locator("h1")).toHaveText("You are in");

  const link = await waitForLink(personEmail, "talent network", /https?:\/\/\S+?\/talent\/profile\/[A-Za-z0-9_-]+/);
  await personPage.goto(link);
  await expect(personPage.locator("h1")).toHaveText("Hi Grace Hopper");
  await expect(personPage.locator(".standing-on")).toContainText("You are in the network");
  // The recruiter's overview lists them.
  await recruiterPage.goto("/app/talent");
  await expect(recruiterPage.locator("table").last()).toContainText("Grace Hopper");
});

test("the company issues itself a token on the developer page", async () => {
  await clientPage.goto("/client/developer");
  await expect(clientPage.locator("h1")).toHaveText("Your candidates, by API");
  await clientPage.getByLabel("Name").fill("E2E integration");
  await clientPage.getByRole("button", { name: "Issue token" }).click();
  const secret = await clientPage.locator("code.secret").innerText();
  expect(secret.length).toBeGreaterThan(20);
  clientToken = secret.trim();
  await expect(clientPage.locator("table")).toContainText("E2E integration");
});

test("the company files a request through the API and asks to meet the match", async () => {
  const api = await request.newContext({
    baseURL: fixture.baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${clientToken}` },
  });
  const me = await json("portal me", await api.get("/api/v1/portal/me"));
  expect(me.client_company_id).toBe(fixture.clientCompanyId);
  const created = await json(
    "file a talent request",
    await api.post("/api/v1/portal/talent-requests", {
      data: { title: "Platform engineer", skills: ["go", "kubernetes"], seniority: "senior", remote_policy: "remote", job_id: jobId },
    }),
  );
  requestId = created.request.id;
  const matches = await json("read matches", await api.get(`/api/v1/portal/talent-requests/${requestId}/matches`));
  expect(matches.matches.length).toBeGreaterThanOrEqual(1);
  const match = matches.matches[0];
  expect(match.source).toBe("network");
  expect(match.shared_skills).toEqual(["go", "kubernetes"]);
  expect(match.headline).toBe("Compiler engineer who runs clusters");
  expect(JSON.stringify(match)).not.toContain("Grace");
  expect(JSON.stringify(match)).not.toContain(personEmail);
  matchId = match.id;
  const intro = await json("ask for an introduction", await api.post(`/api/v1/portal/talent-requests/${requestId}/matches/${matchId}/introduce`));
  expect(intro.status).toBe("requested");
  expect(intro.label).not.toContain("Grace");
  introId = intro.id;
  // The portal page shows the same, still anonymous.
  await clientPage.goto(`/client/talent/${requestId}`);
  await expect(clientPage.locator("h1")).toHaveText("Platform engineer");
  await expect(clientPage.locator(".talent-intros")).toContainText("Asked");
  await expect(clientPage.locator("body")).not.toContainText("Grace Hopper");
  await api.dispose();
});

test("the recruiter sees the introduction waiting and sends the opportunity", async () => {
  await recruiterPage.goto("/app/queue");
  await expect(recruiterPage.locator(".queue")).toContainText("Grace Hopper");
  await recruiterPage.locator(".queue-row", { hasText: "Grace Hopper" }).getByRole("link", { name: "Send opportunity" }).click();
  await expect(recruiterPage.locator("h1")).toHaveText("Platform engineer");
  const row = recruiterPage.locator(".intro-table tr", { hasText: "Grace Hopper" });
  await expect(row).toContainText("To send");
  await row.getByRole("button", { name: "Send opportunity" }).click();
  await expect(recruiterPage.locator(".flash-success")).toContainText("on its way");
  await expect(recruiterPage.locator(".intro-table tr", { hasText: "Grace Hopper" })).toContainText("Sent");
});

test("the person says yes and the company reads the new candidate", async () => {
  const link = await waitForLink(personEmail, "are you interested", /https?:\/\/\S+?\/opportunity\/[A-Za-z0-9_-]+/);
  await personPage.goto(link);
  await expect(personPage.locator("h1")).toContainText("Acme E2E");
  await personPage.getByRole("button", { name: "I'm interested" }).click();
  await expect(personPage.locator("h2")).toHaveText("You are in the running");

  await clientPage.goto(`/client/talent/${requestId}`);
  const intro = clientPage.locator(".talent-intros tr", { hasText: "Grace Hopper" });
  await expect(intro).toContainText("Said yes");
  await intro.getByRole("link", { name: "Grace Hopper" }).click();
  await expect(clientPage.locator("h1")).toHaveText("Grace Hopper");
  await expect(clientPage.locator("body")).toContainText(personEmail);

  const api = await clientPage.context().request.get("/api/v1/portal/events?since=0", {
    headers: { Authorization: `Bearer ${clientToken}` },
  });
  await must("read the feed", api);
  const feed = await api.json();
  const released = feed.events.find((e: { kind: string; job_id: string }) => e.kind === "released" && e.job_id === jobId);
  expect(released).toBeTruthy();
  expect(feed.next_since).toBeGreaterThan(0);
  const apps = await clientPage.context().request.get(`/api/v1/portal/applications?job_id=${jobId}`, {
    headers: { Authorization: `Bearer ${clientToken}` },
  });
  await must("list the company's applications", apps);
  const list = await apps.json();
  expect(list.applications.map((a: { candidate: { label: string } }) => a.candidate.label)).toContain("Grace Hopper");
  expect(introId).toBeTruthy();
});
