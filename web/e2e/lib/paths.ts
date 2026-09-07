import path from "node:path";

/** The web/e2e directory. */
export const e2eDir = path.resolve(__dirname, "..");

/** Scratch space run.sh and the suite share; ignored by git. */
export const runDir = path.join(e2eDir, ".run");

/** The repository root. */
export const repoRoot = path.resolve(e2eDir, "..", "..");

/** Where the bootstrap writes what the specs need to sign in and navigate. */
export const fixturePath = path.join(runDir, "fixture.json");

/** The recruiter's signed-in browser state, reused by every recruiter spec. */
export const recruiterStatePath = path.join(runDir, "recruiter-state.json");
