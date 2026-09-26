import assert from "node:assert/strict";
import { test } from "node:test";

import { clockOffset, formatCountdown, headline, lastEnded, nextMine, type SprintState } from "./clock";
import { isPolite } from "./rtc";

const base: SprintState = {
  now: 1_000_000,
  status: "scheduled",
  phase: "round",
  round: 1,
  rounds: 3,
  next: 1_000_000 + 90_000,
  starts_at: 1_000_000 - 400_000,
  ends_at: 1_000_000 + 700_000,
  round_seconds: 300,
  break_seconds: 60,
  mine: [
    { pairing: "a", round: 0, with: "Ada", starts_at: 1_000_000 - 400_000, ends_at: 1_000_000 - 100_000, room: "/r/a", rated: false },
    { pairing: "b", round: 1, with: "Bob", starts_at: 1_000_000 - 40_000, ends_at: 1_000_000 + 260_000, room: "/r/b", rated: false },
    { pairing: "c", round: 2, with: "Cy", starts_at: 1_000_000 + 320_000, ends_at: 1_000_000 + 620_000, room: "/r/c", rated: false },
  ],
  current: null,
};

test("countdown never goes negative and pads seconds", () => {
  assert.equal(formatCountdown(0), "0:00");
  assert.equal(formatCountdown(-5000), "0:00");
  assert.equal(formatCountdown(61_000), "1:01");
  assert.equal(formatCountdown(299_500), "5:00");
});

test("the offset corrects a fast browser clock", () => {
  assert.equal(clockOffset(base, 1_000_500), 500);
});

test("next and last conversations are read off the server clock", () => {
  assert.equal(nextMine(base, base.now)?.pairing, "b");
  assert.equal(lastEnded(base, base.now)?.pairing, "a");
  assert.equal(nextMine(base, base.now + 700_000), null);
  assert.equal(lastEnded(base, base.now - 500_000), null);
});

test("headlines follow the phase", () => {
  assert.equal(headline({ ...base, phase: "before" }, base.now - 400_000 - 30_000), "Starts in 0:30");
  assert.equal(headline(base, base.now), "Round 2 of 3 · 1:30 left");
  assert.equal(headline({ ...base, phase: "break", next: base.now + 10_000 }, base.now), "Break · round 3 starts in 0:10");
  assert.equal(headline({ ...base, phase: "after" }, base.now), "The sprint has ended.");
});

test("exactly one side of a pair is polite", () => {
  assert.equal(isPolite("pa", "pb"), true);
  assert.equal(isPolite("pb", "pa"), false);
  assert.equal(isPolite("pa", "pa"), false);
});
