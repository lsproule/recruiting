// What the island does depends on why it is on the page. An attempt is the
// recorded, timed, submittable thing; try mode is the same editor with none
// of that — an author checking a problem leaves no attempt behind.

export type Mode = "attempt" | "try";

export interface Features {
  recorder: boolean;
  beacon: boolean;
  timer: boolean;
  consent: boolean;
  run: boolean;
  submit: boolean;
}

export function sessionFeatures(mode: Mode): Features {
  const attempt = mode !== "try";
  return { recorder: attempt, beacon: attempt, timer: attempt, consent: attempt, run: true, submit: attempt };
}

// executePath is the API path, under the config's base, for one execution.
// An attempt's runs are scoped to the attempt they belong to; try mode has no
// attempt, so it posts to the problem's own try endpoint, which runs the
// public cases and writes neither a submission nor a recording.
export function executePath(mode: Mode, attemptID: string, problemID: string, kind: "run" | "submit"): string {
  if (mode === "try") {
    if (kind === "submit") throw new Error("try mode has no submit");
    return "/problems/" + problemID + "/try";
  }
  return "/attempts/" + attemptID + "/problems/" + problemID + "/" + kind;
}
