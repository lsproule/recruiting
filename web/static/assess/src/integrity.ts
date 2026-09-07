// Integrity: the measures a candidate agreed to before the timer started.
// The scheduling, the jittered beat, and the shape of the events they
// produce live here, away from the DOM, so they can be exercised without a
// camera. Capturing a frame is camera.ts; wiring is main.ts.

export interface IntegrityConfig {
  fullscreen: boolean;
  block_paste: boolean;
  webcam: boolean;
  webcam_interval_s: number;
  photo_id: boolean;
  snapshot_url: string;
  consent_url: string;
}

export const DEFAULT_WEBCAM_INTERVAL_S = 60;
export const MIN_WEBCAM_INTERVAL_S = 15;
export const MAX_WEBCAM_INTERVAL_S = 600;

// Beats are spread ±10s so the frames do not land on a predictable clock.
export const SNAPSHOT_JITTER_MS = 10_000;

export const SNAPSHOT_MAX_WIDTH = 640;
export const SNAPSHOT_QUALITY = 0.6;
export const SNAPSHOT_TYPE = "image/jpeg";

export function integrityOn(i: IntegrityConfig | undefined | null): boolean {
  return !!i && (i.fullscreen || i.block_paste || i.webcam || i.photo_id);
}

// snapshotDelay is how long until the next beat: the configured interval,
// held to the bounds the assessment form enforces, jittered either way.
export function snapshotDelay(intervalS: number, rand: () => number = Math.random): number {
  const seconds = intervalS > 0 ? Math.min(Math.max(intervalS, MIN_WEBCAM_INTERVAL_S), MAX_WEBCAM_INTERVAL_S) : DEFAULT_WEBCAM_INTERVAL_S;
  return seconds * 1000 + Math.round((rand() * 2 - 1) * SNAPSHOT_JITTER_MS);
}

// EMPTY_SHA256 is the digest of the empty string: a blocked paste carries no
// text, and the recording's paste shape still wants a digest.
export const EMPTY_SHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

export interface PasteEventData {
  len: number;
  sha256: string;
  internal: boolean;
  blocked?: boolean;
}

// blockedPaste is what a paste the session refused looks like in the
// recording: nothing arrived, and the refusal is the point.
export function blockedPaste(): PasteEventData {
  return { len: 0, sha256: EMPTY_SHA256, internal: false, blocked: true };
}

export interface SnapshotSink {
  // capture answers with one JPEG frame, or null when the camera has none.
  capture(): Promise<Blob | null>;
  upload(seq: number, frame: Blob): Promise<void>;
  // missed records the beat that produced no frame.
  missed(seq: number): void;
}

export interface Timers {
  set(fn: () => void, ms: number): number;
  clear(id: number): void;
}

export const realTimers: Timers = {
  set: (fn, ms) => window.setTimeout(fn, ms),
  clear: (id) => window.clearTimeout(id),
};

// SnapshotLoop takes one frame per jittered beat for as long as the session
// runs. A beat that cannot produce or store a frame is recorded as a gap and
// the loop carries on: a broken camera never interrupts the assessment.
export class SnapshotLoop {
  constructor(
    private readonly intervalS: number,
    private readonly sink: SnapshotSink,
    private readonly timers: Timers = realTimers,
    private readonly rand: () => number = Math.random,
  ) {}

  private seq = 0;
  private timer: number | null = null;
  private stopped = false;

  start(): void {
    if (this.timer === null && !this.stopped) this.arm();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== null) {
      this.timers.clear(this.timer);
      this.timer = null;
    }
  }

  private arm(): void {
    this.timer = this.timers.set(() => void this.beat(), snapshotDelay(this.intervalS, this.rand));
  }

  private async beat(): Promise<void> {
    this.timer = null;
    const seq = this.seq++;
    try {
      const frame = await this.sink.capture();
      if (!frame) throw new Error("the camera produced no frame");
      await this.sink.upload(seq, frame);
    } catch {
      // A missed beat is a gap in the recording, never an interruption.
      this.sink.missed(seq);
    }
    if (!this.stopped) this.arm();
  }
}
