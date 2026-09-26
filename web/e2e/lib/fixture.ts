import fs from "node:fs";

import { fixturePath } from "./paths";

/** Fixture is what the bootstrap leaves behind for the specs to work from. */
export interface Fixture {
  baseURL: string;
  adminEmail: string;
  adminPassword: string;
  /** The org's URL slug: the public talent-network page lives under it. */
  orgSlug: string;
  clientEmail: string;
  clientPassword: string;
  apiToken: string;
  clientCompanyId: string;
  jobId: string;
  applicationId: string;
  assessmentId: string;
  problemId: string;
  problemTitle: string;
  candidateEmail: string;
  /** The candidate's one-time entry link, straight out of the invite email. */
  assessURL: string;
  /** The admin's own user id, and the vetter the bootstrap adds beside them. */
  adminUserId: string;
  vetterUserId: string;
  vetterEmail: string;
  vetterPassword: string;
  vetterToken: string;
  /** The built-in processes, by library key. */
  processes: Record<string, string>;
}

export function writeFixture(f: Fixture): void {
  fs.writeFileSync(fixturePath, JSON.stringify(f, null, 2));
}

export function readFixture(): Fixture {
  return JSON.parse(fs.readFileSync(fixturePath, "utf8")) as Fixture;
}
