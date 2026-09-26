import fs from "node:fs";

import { APIRequestContext, request } from "@playwright/test";

import { Fixture, writeFixture } from "./lib/fixture";
import { waitForLink } from "./lib/mailbox";
import { recruiterStatePath, runDir } from "./lib/paths";

// The one problem the whole suite works against: an easy one, so its seed
// bank entry carries a rust reference solution as well as a python one.
const PROBLEM_TITLE = "Receipt Total";

const ADMIN_PASSWORD = "e2e-admin-password";
const CLIENT_PASSWORD = "e2e-client-password";
const VETTER_PASSWORD = "e2e-vetter-password";

/** csrfToken pulls the double-submit token out of a rendered form. */
function csrfToken(html: string): string {
  const m = html.match(/name="_csrf" value="([^"]+)"/);
  if (!m) throw new Error("no CSRF field in the rendered page");
  return m[1];
}

/** must fails loudly with the body, which is where the app puts its reason. */
async function must(label: string, res: { ok(): boolean; status(): number; text(): Promise<string> }) {
  if (!res.ok()) throw new Error(`${label}: ${res.status()} ${(await res.text()).slice(0, 500)}`);
}

/** setPassword walks a one-time password link and chooses a password. */
async function setPassword(api: APIRequestContext, link: string, password: string) {
  const page = await api.get(link);
  await must(`open ${link}`, page);
  const res = await api.post(link, {
    form: { _csrf: csrfToken(await page.text()), password },
    maxRedirects: 0,
  });
  if (res.status() !== 303) throw new Error(`set password: ${res.status()} ${(await res.text()).slice(0, 300)}`);
}

async function signIn(api: APIRequestContext, prefix: string, email: string, password: string) {
  const page = await api.get(`${prefix}/login`);
  await must("open the sign-in page", page);
  const res = await api.post(`${prefix}/login`, {
    form: { _csrf: csrfToken(await page.text()), email, password },
    maxRedirects: 0,
  });
  if (res.status() !== 303) throw new Error(`sign in as ${email}: ${res.status()}`);
}

/** userIDs reads the org's users off the API token screen, by email. */
async function userIDs(api: APIRequestContext): Promise<Record<string, string>> {
  const page = await api.get("/app/admin/api-tokens");
  await must("open the API token screen", page);
  const out: Record<string, string> = {};
  for (const m of (await page.text()).matchAll(/<option value="([0-9a-f-]{36})">[^<]*&lt;([^&]+)&gt;<\/option>/g)) {
    out[m[2]] = m[1];
  }
  return out;
}

/** issueAPIToken issues a token for one user and returns its secret. */
async function issueAPIToken(api: APIRequestContext, userID: string): Promise<string> {
  const page = await api.get("/app/admin/api-tokens");
  await must("open the API token screen", page);
  const html = await page.text();
  const res = await api.post("/app/admin/api-tokens", {
    form: { _csrf: csrfToken(html), name: "e2e", user_id: userID },
  });
  await must("issue an API token", res);
  // The screen shows the raw secret once, in the first <code> of its notice.
  const secret = (await res.text()).match(/<code>([A-Za-z0-9_-]{20,})<\/code>/);
  if (!secret) throw new Error("the API token screen showed no secret");
  return secret[1];
}

/** createOrgUser adds an org user with roles and sets their password. */
async function createOrgUser(api: APIRequestContext, name: string, email: string, roles: string[], password: string) {
  const page = await api.get("/app/admin/users");
  await must("open the users screen", page);
  const res = await api.post("/app/admin/users", {
    form: { _csrf: csrfToken(await page.text()), name, email, roles },
  });
  await must("create an org user", res);
  const link = (await res.text()).match(/https?:\/\/\S+?\/app\/reset\/[A-Za-z0-9_-]+/);
  if (!link) throw new Error("no password-set link for the org user");
  await setPassword(api, link[0], password);
}

/** createClientCompany adds a company and returns its id. */
async function createClientCompany(api: APIRequestContext, name: string): Promise<string> {
  const page = await api.get("/app/admin/clients");
  await must("open the client admin screen", page);
  const res = await api.post("/app/admin/clients", {
    form: { _csrf: csrfToken(await page.text()), name },
  });
  await must("add a client company", res);
  const html = await res.text();
  const opt = html.match(new RegExp(`<option value="([0-9a-f-]{36})"[^>]*>${name}<`));
  if (!opt) throw new Error(`the client admin screen did not list ${name}`);
  return opt[1];
}

/** createClientUser adds a portal user and sets its password. */
async function createClientUser(api: APIRequestContext, companyID: string, email: string) {
  const page = await api.get("/app/admin/clients");
  const res = await api.post("/app/admin/clients/users", {
    form: {
      _csrf: csrfToken(await page.text()),
      name: "E2E Client",
      email,
      client_company_id: companyID,
    },
  });
  await must("create a client user", res);
  // The screen prints the one-time link so an unconfigured mailer cannot
  // strand the new account.
  const link = (await res.text()).match(/https?:\/\/\S+?\/app\/reset\/[A-Za-z0-9_-]+/);
  if (!link) throw new Error("no password-set link for the client user");
  await setPassword(api, link[0], CLIENT_PASSWORD);
}

export default async function globalSetup() {
  const baseURL = process.env.E2E_BASE_URL;
  const passwordSetURL = process.env.E2E_PASSWORD_SET_URL;
  const adminEmail = process.env.E2E_ADMIN_EMAIL;
  if (!baseURL || !passwordSetURL || !adminEmail) {
    throw new Error("run the suite through web/e2e/run.sh; it boots the app and bootstraps the org");
  }
  fs.mkdirSync(runDir, { recursive: true });

  const stamp = Date.now();
  const candidateEmail = `e2e-candidate-${stamp}@example.test`;
  const clientEmail = `e2e-client-${stamp}@example.test`;

  const api = await request.newContext({ baseURL });
  await setPassword(api, passwordSetURL, ADMIN_PASSWORD);
  await signIn(api, "/app", adminEmail, ADMIN_PASSWORD);

  const vetterEmail = `e2e-vetter-${stamp}@example.test`;
  await createOrgUser(api, "Vera Vetter", vetterEmail, ["vetter"], VETTER_PASSWORD);
  const users = await userIDs(api);
  const adminUserId = users[adminEmail];
  const vetterUserId = users[vetterEmail];
  if (!adminUserId || !vetterUserId) throw new Error(`the token screen lists ${Object.keys(users)}, not the admin and the vetter`);
  const token = await issueAPIToken(api, adminUserId);
  const vetterToken = await issueAPIToken(api, vetterUserId);
  const clientCompanyId = await createClientCompany(api, "Acme E2E");
  await createClientUser(api, clientCompanyId, clientEmail);

  // Everything the browser scenarios need but never exercise goes through
  // the JSON API, which is far steadier than clicking it into place.
  const asAPI = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${token}` },
  });
  const json = async (label: string, res: Awaited<ReturnType<APIRequestContext["post"]>>) => {
    await must(label, res);
    return res.json();
  };

  // The vetter is bookable around the clock, so a booking scenario always
  // finds a slot two hours out whatever the wall clock says.
  const asVetter = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${vetterToken}` },
  });
  await must(
    "set the vetter's availability",
    await asVetter.put("/api/v1/availability", {
      data: {
        timezone: "UTC",
        slot_minutes: 30,
        buffer_minutes: 0,
        rules: [0, 1, 2, 3, 4, 5, 6].map((weekday) => ({ weekday, start: "00:00", end: "23:30" })),
      },
    }),
  );
  await asVetter.dispose();

  const processList = await json("list processes", await asAPI.get("/api/v1/processes"));
  const processes: Record<string, string> = {};
  for (const p of processList.processes as { id: string; library_key?: string }[]) {
    if (p.library_key) processes[p.library_key] = p.id;
  }

  const problems = await json("list problems", await asAPI.get("/api/v1/problems"));
  const problem = problems.problems.find((p: { title: string }) => p.title === PROBLEM_TITLE);
  if (!problem) throw new Error(`the seeded bank has no ${PROBLEM_TITLE}`);

  const job = await json(
    "create a job",
    await asAPI.post("/api/v1/jobs", {
      data: {
        client_company_id: clientCompanyId,
        title: `E2E Engineer ${stamp}`,
        status: "open",
      },
    }),
  );

  const stages = await json("list stages", await asAPI.get(`/api/v1/jobs/${job.id}/stages`));
  const stage = stages.stages.find((s: { kind: string }) => s.kind === "assessment");
  if (!stage) throw new Error("the default pipeline has no assessment stage");

  const assessment = await json(
    "create an assessment",
    await asAPI.post("/api/v1/assessments", {
      data: {
        name: "E2E problem set",
        duration_minutes: 60,
        invite_window_days: 7,
        problem_ids: [problem.id],
        // The shortest interval the service accepts, so the snapshot
        // scenario does not sit through a production-length wait.
        integrity: { fullscreen: true, webcam: true, webcam_interval_s: 15, photo_id: true },
      },
    }),
  );
  await must(
    "attach the assessment to its stage",
    await asAPI.put(`/api/v1/jobs/${job.id}/stages/${stage.id}/assessment`, {
      data: { assessment_id: assessment.id },
    }),
  );

  await json(
    "create a candidate",
    await asAPI.post("/api/v1/candidates", {
      data: { name: "E2E Candidate", email: candidateEmail, job_id: job.id },
    }),
  );
  const apps = await json("list applications", await asAPI.get(`/api/v1/jobs/${job.id}/applications`));
  const application = apps.applications[0];
  if (!application) throw new Error("adding the candidate opened no application");

  // Entering the assessment stage is what queues the invite email.
  await must(
    "move the application into the assessment stage",
    await asAPI.post(`/api/v1/applications/${application.id}/move`, {
      data: { to_stage_id: stage.id, reason: "browser end-to-end run", override_prereq: true },
    }),
  );
  const assessURL = await waitForLink(candidateEmail, "assessment", /https?:\/\/\S+?\/assess\/[A-Za-z0-9_-]+/);

  await api.storageState({ path: recruiterStatePath });

  const fixture: Fixture = {
    baseURL,
    adminEmail,
    adminPassword: ADMIN_PASSWORD,
    clientEmail,
    clientPassword: CLIENT_PASSWORD,
    apiToken: token,
    clientCompanyId,
    jobId: job.id,
    applicationId: application.id,
    assessmentId: assessment.id,
    problemId: problem.id,
    problemTitle: PROBLEM_TITLE,
    candidateEmail,
    assessURL,
    adminUserId,
    vetterUserId,
    vetterEmail,
    vetterPassword: VETTER_PASSWORD,
    vetterToken,
    processes,
  };
  writeFixture(fixture);

  await api.dispose();
  await asAPI.dispose();
}
