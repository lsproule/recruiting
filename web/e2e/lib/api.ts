import { APIRequestContext, request } from "@playwright/test";

import { Fixture } from "./fixture";

/** must fails loudly with the body, which is where the app puts its reason. */
export async function must(label: string, res: { ok(): boolean; status(): number; text(): Promise<string> }) {
  if (!res.ok()) throw new Error(`${label}: ${res.status()} ${(await res.text()).slice(0, 500)}`);
}

/** json is must plus the parsed body. */
export async function json<T = any>(label: string, res: Awaited<ReturnType<APIRequestContext["get"]>>): Promise<T> {
  await must(label, res);
  return (await res.json()) as T;
}

/** asAdmin is an API context carrying the bootstrap's admin token. */
export function asAdmin(fixture: Fixture): Promise<APIRequestContext> {
  return request.newContext({
    baseURL: fixture.baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${fixture.apiToken}` },
  });
}

/** asVetter is an API context carrying the vetter's token. */
export function asVetter(fixture: Fixture): Promise<APIRequestContext> {
  return request.newContext({
    baseURL: fixture.baseURL,
    extraHTTPHeaders: { Authorization: `Bearer ${fixture.vetterToken}` },
  });
}

/** createJob makes a job on the fixture's client from a process. */
export async function createJob(api: APIRequestContext, fixture: Fixture, title: string, processId?: string): Promise<{ id: string; stages: any[] }> {
  const job = await json("create a job", await api.post("/api/v1/jobs", {
    data: { client_company_id: fixture.clientCompanyId, title, status: "open", process_id: processId },
  }));
  const stages = await json("list stages", await api.get(`/api/v1/jobs/${job.id}/stages`));
  return { id: job.id, stages: stages.stages };
}

/** addCandidate opens an application for a new candidate and returns it. */
export async function addCandidate(api: APIRequestContext, jobId: string, name: string, email: string): Promise<{ id: string }> {
  await json("create a candidate", await api.post("/api/v1/candidates", { data: { name, email, job_id: jobId } }));
  const apps = await json("list applications", await api.get(`/api/v1/jobs/${jobId}/applications`));
  const app = apps.applications.find((a: { candidate_email: string }) => a.candidate_email === email);
  if (!app) throw new Error(`no application for ${email}`);
  return app;
}

/** move puts an application into a stage, overriding any prerequisite. */
export async function move(api: APIRequestContext, applicationId: string, stageId: string) {
  await must(
    "move the application",
    await api.post(`/api/v1/applications/${applicationId}/move`, {
      data: { to_stage_id: stageId, reason: "browser end-to-end run", override_prereq: true },
    }),
  );
}
