import fs from "node:fs";

import { fixturePath } from "./paths";

/** Fixture is what the bootstrap leaves behind for the specs to work from. */
export interface Fixture {
  baseURL: string;
  adminEmail: string;
  adminPassword: string;
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
}

export function writeFixture(f: Fixture): void {
  fs.writeFileSync(fixturePath, JSON.stringify(f, null, 2));
}

export function readFixture(): Fixture {
  return JSON.parse(fs.readFileSync(fixturePath, "utf8")) as Fixture;
}
