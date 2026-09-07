// Reviewer replay island: rebuilds each editor from the recorded changesets
// and scrubs the sitting. Read-only — nothing here writes to the attempt.
//
// Changesets are CodeMirror's own, so ChangeSet.fromJSON applies them in the
// UTF-16 units the recorder and the integrity signals both count in. Folding
// the whole stream on every scrub would be quadratic, so every SNAPSHOT_EVERY
// edits of a problem the document is kept and later scrubs start from the
// nearest one.

import { ChangeSet, EditorState, Text } from "@codemirror/state";
import { EditorView, lineNumbers, drawSelection } from "@codemirror/view";

interface Config {
  attempt_id: string;
  manifest_url: string;
  // Where the webcam frames behind the snapshot markers are fetched from.
  // Their links are signed and short-lived, so they are asked for when a
  // marker is opened rather than baked into the page.
  snapshots_url?: string;
  // The jump chips, derived server-side from the submissions. Each names a
  // moment in wall-clock milliseconds, the same clock the events carry.
  jumps?: Jump[];
  // readonly marks a viewer outside the org: the manifest it reads carries
  // the code alone, so the timeline is described as runs and submits and no
  // webcam frame is ever asked for.
  readonly?: boolean;
}

interface Jump {
  key: string;
  label: string;
  at: number;
}

interface Frame {
  seq: number;
  taken_at: number;
  url: string;
}

interface ReplayEvent {
  seq: number;
  kind: string;
  problem_id?: string;
  at: number;
  payload: unknown;
}

interface Marker {
  seq: number;
  kind: string;
  problem_id: string;
  at: number;
  note: string;
  // The webcam beat this marker opens; absent or zero when it opens nothing.
  snapshot_seq?: number;
}

interface Problem {
  id: string;
  title: string;
  language: string;
  initial_source: string;
  final_source: string;
}

interface Manifest {
  attempt_id: string;
  status: string;
  recording_status: string;
  started_at: number;
  finished_at: number;
  snapshot_every: number;
  problems: Problem[];
  events: ReplayEvent[];
  markers: Marker[];
  next_after_seq: number;
}

// changesOf reads the changeset out of an edit payload. The recorder posts
// ChangeSet.toJSON() bare; the stream also accepts it under a "changes" key.
function changesOf(payload: unknown): unknown[] | null {
  if (Array.isArray(payload)) return payload;
  if (payload && typeof payload === "object") {
    const wrapped = (payload as { changes?: unknown }).changes;
    if (Array.isArray(wrapped)) return wrapped;
  }
  return null;
}

function el(tag: string, className?: string, text?: string): HTMLElement {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// Track is one problem's editable history: its edits in seq order and the
// documents kept along the way.
class Track {
  readonly seqs: number[] = [];
  // diverged is set once a changeset would not parse or would not fit the
  // document it applies to. What the viewer shows from there on is no longer
  // the text the candidate saw.
  diverged = false;
  private readonly sets: ChangeSet[] = [];
  private readonly snapshots: Text[] = [];

  constructor(
    private readonly initial: Text,
    private readonly every: number,
  ) {
    this.snapshots.push(initial);
  }

  add(seq: number, changes: ChangeSet): void {
    this.seqs.push(seq);
    this.sets.push(changes);
  }

  get edits(): number {
    return this.sets.length;
  }

  // applied is how many of this problem's edits land at or before seq.
  applied(seq: number): number {
    let lo = 0;
    let hi = this.seqs.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (this.seqs[mid] <= seq) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }

  // docAt is the document after the first n edits. A changeset that does not
  // fit the document it is applied to stops the replay there rather than
  // throwing: the recording has a gap, and the rest of it says nothing.
  docAt(n: number): Text {
    const count = Math.max(0, Math.min(n, this.sets.length));
    let index = Math.min(Math.floor(count / this.every), this.snapshots.length - 1);
    let doc = this.snapshots[index];
    let at = index * this.every;
    while (at < count) {
      try {
        doc = this.sets[at].apply(doc);
      } catch {
        this.diverged = true;
        return doc;
      }
      at += 1;
      if (at % this.every === 0) {
        index = at / this.every;
        if (this.snapshots.length === index) this.snapshots.push(doc);
      }
    }
    return doc;
  }

  get initialDoc(): Text {
    return this.initial;
  }
}

function stamp(at: number, startedAt: number): string {
  if (!startedAt || !at) return "";
  const secs = Math.max(0, Math.round((at - startedAt) / 1000));
  const m = Math.floor(secs / 60);
  return String(m).padStart(2, "0") + ":" + String(secs % 60).padStart(2, "0");
}

// The playback speeds the transport offers, and how often it ticks. A tick
// is short enough that 4x still lands on individual events.
const SPEEDS = [1, 1.5, 2, 4];
const TICK_MS = 80;

// Frames fetches the webcam frames behind the snapshot markers, once. Their
// links are signed and expire within minutes, so a reviewer who leaves the
// page open long enough is told to reload rather than shown a dead image.
class Frames {
  private pending: Promise<Map<number, Frame>> | null = null;

  constructor(private readonly url?: string) {}

  get(seq: number): Promise<Frame | undefined> {
    if (!this.url) return Promise.resolve(undefined);
    if (!this.pending) {
      const url = this.url;
      this.pending = fetch(url, { headers: { Accept: "application/json" }, credentials: "same-origin" })
        .then((res) => {
          if (!res.ok) throw new Error("the webcam frames could not be loaded (" + res.status + ")");
          return res.json() as Promise<Frame[]>;
        })
        .then((list) => new Map((list ?? []).map((f) => [f.seq, f])));
    }
    return this.pending.then((bySeq) => bySeq.get(seq));
  }
}

// openFrame shows one webcam beat under the timeline. The link is minted for
// this reader and expires shortly, so it is never offered as something to
// copy — only as the image itself.
async function openFrame(frames: Frames, host: HTMLElement, seq: number, caption: string): Promise<void> {
  host.textContent = "";
  host.hidden = false;
  try {
    const frame = await frames.get(seq);
    if (!frame?.url) {
      host.appendChild(el("p", "muted", "That beat stored no frame."));
      return;
    }
    const img = document.createElement("img");
    img.src = frame.url;
    img.alt = "Webcam frame " + seq;
    img.className = "replay-frame-image";
    img.addEventListener("error", () => {
      host.textContent = "";
      host.appendChild(el("p", "muted", "The link to this frame has expired. Reload the page to look again."));
    });
    const close = el("button", "replay-frame-close", "Close");
    close.setAttribute("type", "button");
    close.addEventListener("click", () => {
      host.hidden = true;
      host.textContent = "";
    });
    host.append(img, el("span", "replay-frame-caption", caption), close);
  } catch (err) {
    host.textContent = "";
    host.appendChild(el("p", "muted", (err as Error).message));
  }
}

// MAX_PAGES bounds the paging loop so a server that kept handing back the
// same cursor could not spin the browser forever.
const MAX_PAGES = 1000;

async function fetchPage(url: string, afterSeq: number): Promise<Manifest> {
  const target = new URL(url, window.location.href);
  if (afterSeq > 0) target.searchParams.set("after_seq", String(afterSeq));
  const res = await fetch(target.toString(), {
    headers: { Accept: "application/json" },
    credentials: "same-origin",
  });
  if (!res.ok) throw new Error("the recording could not be loaded (" + res.status + ")");
  return (await res.json()) as Manifest;
}

// load pages the stream in until the server reports no more. The first page
// carries the editors; later ones add events and markers to it.
async function load(root: HTMLElement, cfg: Config): Promise<Manifest> {
  const first = await fetchPage(cfg.manifest_url, 0);
  let after = first.next_after_seq ?? 0;
  for (let page = 0; after > 0 && page < MAX_PAGES; page += 1) {
    root.textContent = "Loading the recording… " + first.events.length + " events";
    const next = await fetchPage(cfg.manifest_url, after);
    first.events = first.events.concat(next.events ?? []);
    first.markers = (first.markers ?? []).concat(next.markers ?? []);
    if ((next.next_after_seq ?? 0) <= after) break;
    after = next.next_after_seq;
  }
  return first;
}

function mount(root: HTMLElement, cfg: Config): void {
  root.textContent = "Loading the recording…";
  // Evidence anywhere on the page jumps the replay to the event it names.
  // Bound before the stream is in so a click during the load is answered
  // once it lands rather than ignored.
  let jump: ((seq: number) => void) | null = null;
  let pending: number | null = null;
  document.addEventListener("click", (e) => {
    const target = (e.target as HTMLElement | null)?.closest("[data-replay-seq]");
    if (!target) return;
    const seq = Number(target.getAttribute("data-replay-seq"));
    if (!Number.isFinite(seq)) return;
    e.preventDefault();
    if (jump) jump(seq);
    else pending = seq;
  });
  load(root, cfg)
    .then((m) => {
      jump = render(root, m, cfg);
      if (pending !== null && jump) jump(pending);
    })
    .catch((err: Error) => {
      root.textContent = err.message;
    });
}

// render draws the viewer and returns the jump the page's evidence links use.
function render(root: HTMLElement, m: Manifest, cfg: Config): ((seq: number) => void) | null {
  root.textContent = "";
  const every = m.snapshot_every > 0 ? m.snapshot_every : 500;
  const problems = m.problems ?? [];
  const events = m.events ?? [];
  if (problems.length === 0) {
    root.appendChild(el("p", "muted", "This attempt has no problems to replay."));
    return null;
  }

  const tracks = new Map<string, Track>();
  for (const p of problems) tracks.set(p.id, new Track(Text.of((p.initial_source ?? "").split("\n")), every));
  for (const ev of events) {
    if (ev.kind !== "edit" || !ev.problem_id) continue;
    const track = tracks.get(ev.problem_id);
    const changes = changesOf(ev.payload);
    if (!track) continue;
    if (!changes) {
      track.diverged = true;
      continue;
    }
    try {
      track.add(ev.seq, ChangeSet.fromJSON(changes));
    } catch {
      // A changeset that will not parse is skipped, and the track is marked:
      // every document after it is a guess.
      track.diverged = true;
    }
  }

  if (m.recording_status === "incomplete") {
    root.appendChild(el("p", "flash flash-error", "This recording has a gap: some events never reached the server, so the replay is partial."));
  }
  const warning = el("p", "flash flash-error");
  warning.hidden = true;
  root.appendChild(warning);

  // The stream is trusted only as far as it reproduces the text that was
  // actually submitted. Replaying every problem to its end says whether it
  // does, and the reviewer is told when it does not.
  const divergent: string[] = [];
  for (const p of problems) {
    const track = tracks.get(p.id);
    if (!track) continue;
    const replayed = track.docAt(track.edits).toString();
    if (track.diverged || replayed !== (p.final_source ?? "")) divergent.push(p.title);
  }
  if (divergent.length > 0) {
    warning.hidden = false;
    warning.textContent =
      "The replay does not reproduce the submitted code for " +
      divergent.join(", ") +
      ". Read it as an indication only; the submitted source is the record.";
  }

  const tabs = el("div", "replay-tabs");
  root.appendChild(tabs);
  const editorHost = el("div", "replay-editor");
  root.appendChild(editorHost);

  const view = new EditorView({
    state: EditorState.create({
      doc: problems[0].initial_source ?? "",
      extensions: [lineNumbers(), drawSelection(), EditorView.editable.of(false), EditorState.readOnly.of(true)],
    }),
    parent: editorHost,
  });

  // The transport: play/pause and the four speeds. Playback walks the wall
  // clock the events carry, so a pause in the sitting reads as a pause here
  // rather than being compressed away by an even event-per-tick step.
  const controls = el("div", "replay-controls");
  const play = el("button", "replay-play", "Play");
  play.setAttribute("type", "button");
  const speeds = el("div", "replay-speeds");
  const readout = el("span", "replay-readout");
  const speedButtons = new Map<number, HTMLElement>();
  let rate = 1;
  for (const value of SPEEDS) {
    const button = el("button", "replay-speed", value + "\u00d7");
    button.setAttribute("type", "button");
    button.addEventListener("click", () => {
      rate = value;
      for (const [v, b] of speedButtons) b.classList.toggle("on", v === rate);
    });
    button.classList.toggle("on", value === rate);
    speedButtons.set(value, button);
    speeds.appendChild(button);
  }
  controls.append(play, speeds, readout);
  root.appendChild(controls);

  const scrubber = document.createElement("input");
  scrubber.type = "range";
  scrubber.min = "0";
  scrubber.max = String(events.length);
  scrubber.value = String(events.length);
  scrubber.className = "replay-scrubber";
  scrubber.setAttribute("aria-label", "Scrub the recording");
  const timeline = el("div", "replay-timeline");
  const track = el("div", "replay-track");
  track.append(scrubber, timeline);
  root.appendChild(track);

  const jumpBar = el("div", "replay-jumps");
  root.appendChild(jumpBar);
  const frameHost = el("div", "replay-frame");
  frameHost.hidden = true;
  root.appendChild(frameHost);
  const legend = el("p", "replay-legend");
  root.appendChild(legend);

  let current = problems[0].id;
  const tabButtons = new Map<string, HTMLElement>();

  // show puts the editor at position i of the stream: every event up to and
  // including i has happened.
  const show = (i: number): void => {
    const index = Math.max(0, Math.min(i, events.length));
    scrubber.value = String(index);
    const seq = index === 0 ? -1 : events[index - 1].seq;
    const track = tracks.get(current);
    const doc = track ? track.docAt(track.applied(seq)) : Text.empty;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: doc } });
    const at = index === 0 ? m.started_at : events[index - 1].at;
    const problem = problems.find((p) => p.id === current);
    readout.textContent =
      (problem ? problem.title + " \u2014 " : "") +
      "event " + index + " of " + events.length +
      (stamp(at, m.started_at) ? " at " + stamp(at, m.started_at) : "");
    for (const [id, tab] of tabButtons) tab.classList.toggle("active", id === current);
  };

  for (const p of problems) {
    const tab = el("button", "replay-tab", p.title);
    tab.setAttribute("type", "button");
    tab.addEventListener("click", () => {
      current = p.id;
      show(Number(scrubber.value));
    });
    tabButtons.set(p.id, tab);
    tabs.appendChild(tab);
  }

  // indexOfSeq is the scrubber position just after the event with that seq.
  const indexOfSeq = (seq: number): number => {
    for (let i = 0; i < events.length; i += 1) {
      if (events[i].seq >= seq) return i + 1;
    }
    return events.length;
  };

  // indexOfTime is the scrubber position at a moment on the wall clock: the
  // last event that had already happened by then.
  const indexOfTime = (at: number): number => {
    let index = 0;
    for (let i = 0; i < events.length; i += 1) {
      if (events[i].at > at) break;
      index = i + 1;
    }
    return index;
  };

  const jumpToSeq = (seq: number): void => {
    const ev = events.find((e) => e.seq === seq);
    if (ev?.problem_id && tracks.has(ev.problem_id)) current = ev.problem_id;
    stop();
    show(indexOfSeq(seq));
    root.scrollIntoView({ block: "nearest" });
  };

  // Playback. The clock is the sitting's own; a tick advances it by the
  // elapsed wall time times the chosen speed and lands on whatever event
  // that reaches.
  let timer: number | null = null;
  let last = 0;
  let clock = events.length > 0 ? events[events.length - 1].at : m.started_at;
  const stop = (): void => {
    if (timer !== null) window.clearInterval(timer);
    timer = null;
    play.textContent = "Play";
  };
  const tick = (): void => {
    const now = performance.now();
    clock += (now - last) * rate;
    last = now;
    const index = indexOfTime(clock);
    show(index);
    if (index >= events.length) stop();
  };
  play.addEventListener("click", () => {
    if (timer !== null) {
      stop();
      return;
    }
    if (Number(scrubber.value) >= events.length) show(0);
    const index = Number(scrubber.value);
    clock = index === 0 ? m.started_at : events[index - 1].at;
    last = performance.now();
    timer = window.setInterval(tick, TICK_MS);
    play.textContent = "Pause";
  });
  scrubber.addEventListener("input", () => {
    stop();
    show(Number(scrubber.value));
  });

  // The integrity timeline: every marker the server derived, placed on the
  // same track as the scrubber so a blur, a paste, a fullscreen exit and a
  // webcam beat read against the code they happened over.
  const markers = m.markers ?? [];
  const frames = new Frames(cfg.readonly ? undefined : cfg.snapshots_url);
  for (const marker of markers) {
    const index = indexOfSeq(marker.seq);
    const button = el("button", "replay-marker replay-marker-" + marker.kind);
    button.setAttribute("type", "button");
    button.style.left = events.length > 0 ? (index / events.length) * 100 + "%" : "0%";
    const time = stamp(marker.at, m.started_at);
    button.title = (time ? time + " \u2014 " : "") + marker.note;
    button.setAttribute("aria-label", button.title);
    button.addEventListener("click", () => {
      jumpToSeq(marker.seq);
      if (marker.snapshot_seq) void openFrame(frames, frameHost, marker.snapshot_seq, button.title);
      else frameHost.hidden = true;
    });
    timeline.appendChild(button);
  }
  if (cfg.readonly) {
    legend.textContent = markers.length > 0
      ? "The marks on the track are each run and each submit. Click one to jump there."
      : "Nothing was run or submitted.";
  } else {
    legend.textContent = markers.length > 0
      ? "The marks on the track are what the recording flagged: leaving the page, pasting, leaving fullscreen, and each webcam frame. Click one to jump there."
      : "The recording flagged nothing.";
  }

  for (const jump of cfg.jumps ?? []) {
    const chip = el("button", "replay-jump", jump.label);
    chip.setAttribute("type", "button");
    const time = stamp(jump.at, m.started_at);
    if (time) chip.appendChild(el("span", "replay-jump-at", time));
    chip.addEventListener("click", () => {
      stop();
      show(indexOfTime(jump.at));
    });
    jumpBar.appendChild(chip);
  }
  if (jumpBar.childElementCount > 0) jumpBar.prepend(el("span", "replay-jumps-label", "Jump to"));

  show(events.length);
  return jumpToSeq;
}

const cfgEl = document.getElementById("replay-config");
const rootEl = document.getElementById("replay");
if (cfgEl && rootEl) {
  mount(rootEl, JSON.parse(cfgEl.textContent ?? "{}") as Config);
}
