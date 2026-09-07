import test from "node:test";
import assert from "node:assert/strict";
import {
  EMPTY_SHA256,
  MAX_WEBCAM_INTERVAL_S,
  MIN_WEBCAM_INTERVAL_S,
  SNAPSHOT_JITTER_MS,
  SnapshotLoop,
  blockedPaste,
  integrityOn,
  snapshotDelay,
  type SnapshotSink,
  type Timers,
} from "./integrity";
import { sha256Hex } from "./paste";
import { Recorder } from "./recorder";

const noIntegrity = {
  fullscreen: false,
  block_paste: false,
  webcam: false,
  webcam_interval_s: 0,
  photo_id: false,
  snapshot_url: "",
  consent_url: "",
};

test("a session with every measure off asks for no consent", () => {
  assert.equal(integrityOn(undefined), false);
  assert.equal(integrityOn(noIntegrity), false);
  for (const on of ["fullscreen", "block_paste", "webcam", "photo_id"] as const) {
    assert.equal(integrityOn({ ...noIntegrity, [on]: true }), true, on + " should need consent");
  }
});

// The beat is jittered so the frames do not land on a clock a candidate can
// read, but it never drifts outside the interval the recruiter chose by more
// than the jitter.
test("snapshot beats stay within the interval plus or minus the jitter", () => {
  for (const interval of [15, 60, 600]) {
    const centre = interval * 1000;
    assert.equal(snapshotDelay(interval, () => 0), centre - SNAPSHOT_JITTER_MS);
    assert.equal(snapshotDelay(interval, () => 0.5), centre);
    assert.equal(snapshotDelay(interval, () => 1), centre + SNAPSHOT_JITTER_MS);
    for (let i = 0; i < 200; i++) {
      const d = snapshotDelay(interval);
      assert.ok(d >= centre - SNAPSHOT_JITTER_MS && d <= centre + SNAPSHOT_JITTER_MS, "delay " + d + " outside the jitter of " + centre);
    }
  }
});

test("an interval the settings could not carry falls back to the bounds", () => {
  assert.equal(snapshotDelay(0, () => 0.5), 60_000);
  assert.equal(snapshotDelay(1, () => 0.5), MIN_WEBCAM_INTERVAL_S * 1000);
  assert.equal(snapshotDelay(99_999, () => 0.5), MAX_WEBCAM_INTERVAL_S * 1000);
});

// A refused paste is still recorded: nothing arrived, and that is what the
// reviewer needs to see.
test("a blocked paste records no text and says it was blocked", async () => {
  const rec = blockedPaste();
  assert.equal(rec.len, 0);
  assert.equal(rec.blocked, true);
  assert.equal(rec.internal, false);
  assert.equal(rec.sha256, await sha256Hex(""));
  assert.equal(EMPTY_SHA256, rec.sha256);
});

// A fake clock: the loop hands its beat to timers, and the test runs it.
function fakeTimers(): Timers & { run(): Promise<void>; delays: number[]; pending: boolean } {
  let next: (() => void) | null = null;
  return {
    delays: [] as number[],
    get pending() {
      return next !== null;
    },
    set(fn, ms) {
      this.delays.push(ms);
      next = fn;
      return this.delays.length;
    },
    clear() {
      next = null;
    },
    async run() {
      const fn = next;
      next = null;
      if (fn) fn();
      // Let the beat's promise chain settle before the assertions.
      await new Promise((r) => setImmediate(r));
      await new Promise((r) => setImmediate(r));
    },
  };
}

test("each beat uploads one frame under the next sequence number", async () => {
  const uploaded: number[] = [];
  const sink: SnapshotSink = {
    capture: async () => new Blob(["frame"]),
    upload: async (seq) => void uploaded.push(seq),
    missed: () => assert.fail("a captured frame must not be recorded as missed"),
  };
  const timers = fakeTimers();
  const loop = new SnapshotLoop(60, sink, timers, () => 0.5);
  loop.start();
  await timers.run();
  await timers.run();
  assert.deepEqual(uploaded, [0, 1]);
  assert.deepEqual(timers.delays, [60_000, 60_000, 60_000]);
  loop.stop();
});

test("a beat that cannot produce or store a frame is recorded as a gap", async () => {
  const missed: number[] = [];
  let beats = 0;
  const sink: SnapshotSink = {
    capture: async () => {
      beats++;
      if (beats === 1) throw new Error("the camera is gone");
      return new Blob(["frame"]);
    },
    upload: async () => {
      throw new Error("the upload failed");
    },
    missed: (seq) => void missed.push(seq),
  };
  const timers = fakeTimers();
  const loop = new SnapshotLoop(60, sink, timers, () => 0.5);
  loop.start();
  await timers.run();
  await timers.run();
  assert.deepEqual(missed, [0, 1], "both a dead camera and a failed upload are gaps");
  assert.ok(timers.pending, "a failed beat must not end the loop");
  loop.stop();
  assert.equal(timers.pending, false);
});

// The snapshot endpoint appends the snapshot event itself, so the recorder
// has to lift its counter over the server's before it sends the next batch.
test("the recorder resyncs its counter over events the server appended", () => {
  const transport = { post: async () => 0, beacon: () => true };
  const quiet = new Recorder("a1", transport, null);
  quiet.resync(7);
  assert.equal(quiet.record("blur", "p1", {}).seq, 8, "the next event must clear the server's own");

  // An event still waiting to be sent is carried above the server's seq
  // too, or the batch it is in would be refused whole.
  const busy = new Recorder("a2", transport, null);
  const waiting = busy.record("focus", "p1", {});
  busy.resync(7);
  assert.equal(waiting.seq, 8);
  assert.equal(busy.record("blur", "p1", {}).seq, 9);
});
