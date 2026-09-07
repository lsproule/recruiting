// Event recorder: numbers events, batches them to the ingest API, and keeps
// the seq counter and last-acked seq in sessionStorage so a reload continues
// the sequence rather than restarting it (the server refuses any seq at or
// below the last one it stored).

export type EventType =
  | "edit"
  | "paste"
  | "focus"
  | "blur"
  | "run"
  | "submit"
  | "lang_change"
  | "keymap"
  | "fullscreen_enter"
  | "fullscreen_exit"
  | "snapshot"
  | "consent";

export interface AttemptEvent {
  seq: number;
  t: number;
  problem_id: string;
  type: EventType;
  data: unknown;
}

export const FLUSH_INTERVAL_MS = 2000;

// MAX_BATCH matches the server's per-request cap.
export const MAX_BATCH = 500;

export interface RecorderTransport {
  // Posts a batch; resolves with the server's last seq, or rejects. A
  // rejection carrying {lastSeq} means the server is ahead and the client
  // must renumber.
  post(events: AttemptEvent[]): Promise<number>;
  // Fire-and-forget delivery used when the page is being hidden or closed.
  beacon(events: AttemptEvent[]): boolean;
}

export class SeqConflict extends Error {
  constructor(public readonly lastSeq: number) {
    super("seq conflict; server is at " + lastSeq);
  }
}

// Rejected marks a 4xx other than 409: the batch itself is bad and resending
// it would fail the same way, so it is dropped (and logged) rather than
// retried. Any other failure — 5xx, network — is retried.
export class Rejected extends Error {
  constructor(public readonly status: number, detail: string) {
    super("batch rejected (" + status + "): " + detail);
  }
}

export class Recorder {
  private pending: AttemptEvent[] = [];
  private inflight: AttemptEvent[] | null = null;
  private seq: number;
  private acked: number;
  private timer: number | null = null;
  private stopped = false;

  constructor(
    private readonly attemptId: string,
    private readonly transport: RecorderTransport,
    private readonly storage: Storage | null,
    private readonly now: () => number = () => Date.now(),
  ) {
    this.acked = this.readInt("acked");
    this.seq = Math.max(this.readInt("seq"), this.acked);
    this.pending = this.readPending();
  }

  get lastSeq(): number {
    return this.seq;
  }

  record(type: EventType, problemId: string, data: unknown): AttemptEvent {
    if (this.stopped) {
      return { seq: -1, t: this.now(), problem_id: problemId, type, data };
    }
    this.seq += 1;
    const ev: AttemptEvent = { seq: this.seq, t: this.now(), problem_id: problemId, type, data };
    this.pending.push(ev);
    this.writeInt("seq", this.seq);
    this.writePending();
    return ev;
  }

  // resync lifts the counter over events the server appended on its own —
  // a snapshot upload appends one — so the next batch is not refused for a
  // seq the server has already used.
  resync(serverLast: number): void {
    if (serverLast > this.acked) this.renumber([], serverLast);
  }

  start(): void {
    if (this.timer !== null) return;
    this.timer = window.setInterval(() => void this.flush(), FLUSH_INTERVAL_MS);
    document.addEventListener("visibilitychange", this.onVisibility);
    window.addEventListener("pagehide", this.onVisibility);
  }

  // stop ends recording after a final flush; used once the attempt closes.
  async stop(): Promise<void> {
    this.stopped = true;
    if (this.timer !== null) {
      window.clearInterval(this.timer);
      this.timer = null;
    }
    document.removeEventListener("visibilitychange", this.onVisibility);
    window.removeEventListener("pagehide", this.onVisibility);
    await this.flush();
  }

  private onVisibility = (): void => {
    if (document.visibilityState === "hidden") {
      this.beacon();
    }
  };

  // beacon hands the pending batch to sendBeacon, which survives unload. The
  // batch stays pending until the next flush confirms the server has it; a
  // duplicate delivery is refused by seq and then reconciled.
  private beacon(): void {
    const batch = this.pending.concat();
    if (batch.length === 0) return;
    if (this.transport.beacon(batch)) {
      this.acked = batch[batch.length - 1].seq;
      this.writeInt("acked", this.acked);
      this.pending = [];
      this.writePending();
    }
  }

  // flush posts what is pending in chunks of MAX_BATCH, stopping at the
  // first failure so order is kept.
  async flush(): Promise<void> {
    if (this.inflight !== null) return;
    while (this.pending.length > 0) {
      const batch = this.pending.slice(0, MAX_BATCH);
      this.pending = this.pending.slice(MAX_BATCH);
      this.inflight = batch;
      try {
        const last = await this.transport.post(batch);
        this.acked = last;
        this.writeInt("acked", last);
      } catch (err) {
        if (err instanceof SeqConflict) {
          // Renumbered events go on the next tick; looping here would hammer
          // a server that keeps refusing.
          this.renumber(batch, err.lastSeq);
          this.inflight = null;
          this.writePending();
          return;
        } else if (err instanceof Rejected) {
          console.warn("assessment recorder: dropped " + batch.length + " events: " + err.message);
        } else {
          // Server or network trouble: keep the batch, in order, for the next flush.
          this.pending = batch.concat(this.pending);
          this.inflight = null;
          this.writePending();
          return;
        }
      }
      this.inflight = null;
      this.writePending();
    }
  }

  // renumber moves a refused batch above the server's last seq. Nothing is
  // dropped: a counter that fell behind (lost storage) still holds new
  // events, and a rare re-delivery is cheaper than a lost one.
  private renumber(batch: AttemptEvent[], serverLast: number): void {
    const keep = batch.concat(this.pending);
    let next = serverLast;
    for (const e of keep) {
      next += 1;
      e.seq = next;
    }
    this.seq = Math.max(this.seq, next);
    this.acked = serverLast;
    this.pending = keep;
    this.writeInt("seq", this.seq);
    this.writeInt("acked", this.acked);
  }

  private key(name: string): string {
    return "assess:" + this.attemptId + ":" + name;
  }

  private readInt(name: string): number {
    try {
      const v = this.storage?.getItem(this.key(name));
      const n = v ? parseInt(v, 10) : 0;
      return Number.isFinite(n) ? n : 0;
    } catch {
      return 0;
    }
  }

  private writeInt(name: string, n: number): void {
    try {
      this.storage?.setItem(this.key(name), String(n));
    } catch {
      // Storage is a convenience; recording continues without it.
    }
  }

  private readPending(): AttemptEvent[] {
    try {
      const v = this.storage?.getItem(this.key("pending"));
      return v ? (JSON.parse(v) as AttemptEvent[]) : [];
    } catch {
      return [];
    }
  }

  private writePending(): void {
    try {
      this.storage?.setItem(this.key("pending"), JSON.stringify(this.pending));
    } catch {
      // See writeInt.
    }
  }
}
