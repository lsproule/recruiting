import test from "node:test";
import assert from "node:assert/strict";
import { executePath, sessionFeatures } from "./mode";

test("try mode is client-side apart from Run", () => {
  const f = sessionFeatures("try");
  assert.equal(f.recorder, false);
  assert.equal(f.beacon, false);
  assert.equal(f.timer, false);
  assert.equal(f.consent, false);
  assert.equal(f.submit, false);
  assert.equal(f.run, true);
});

test("an attempt records, times, and submits", () => {
  const f = sessionFeatures("attempt");
  for (const [name, on] of Object.entries(f)) assert.equal(on, true, name + " should be on for an attempt");
});

test("an attempt's run and submit are scoped to the attempt", () => {
  assert.equal(executePath("attempt", "a1", "p1", "run"), "/attempts/a1/problems/p1/run");
  assert.equal(executePath("attempt", "a1", "p1", "submit"), "/attempts/a1/problems/p1/submit");
});

// Try mode has no attempt to scope to: it posts to the problem's own try
// endpoint, which runs the public cases and writes nothing.
test("try mode posts to the problem's try endpoint", () => {
  assert.equal(executePath("try", "", "p1", "run"), "/problems/p1/try");
  assert.throws(() => executePath("try", "", "p1", "submit"), /submit/);
});
