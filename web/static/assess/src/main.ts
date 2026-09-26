// Candidate assessment island: one CodeMirror editor per problem, a
// server-enforced countdown, run/submit against the API, and the event
// recording the integrity signals and replay are built on. The same bundle
// mounts in try mode, where an author is checking a problem and none of the
// attempt machinery applies.

import { EditorState, Compartment, Prec } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";
import { Recorder, SeqConflict, Rejected, MAX_BATCH, type AttemptEvent, type EventType } from "./recorder";
import { PasteClassifier } from "./paste";
import { languageLabel, languageSupport } from "./languages";
import { KEYMAPS, KEYMAP_LABELS, applyKeymap, keymapExtension, readKeymap, writeKeymap, type KeymapName } from "./keymap";
import { executePath, sessionFeatures, type Mode } from "./mode";
import { SnapshotLoop, blockedPaste, type IntegrityConfig } from "./integrity";
import { openCamera, uploadFrame, type Camera } from "./camera";
import { mountConsent } from "./consent";

interface TestCase {
  input: string;
  expected: string;
}

interface Problem {
  id: string;
  title: string;
  kind: string;
  statement: string;
  // statement_html is the statement rendered from Markdown by the server.
  // The island never parses Markdown itself; it only paints what it is sent.
  statement_html?: string;
  languages: string[];
  language: string;
  source: string;
  sql_schema: string;
  public_tests: TestCase[];
}

interface Config {
  mode?: Mode;
  format?: string;
  attempt_id: string;
  api_base: string;
  beacon_url: string;
  csrf: string;
  status: string;
  expires_at: number; // unix millis
  integrity?: IntegrityConfig;
  problems: Problem[];
}

interface CandidateTest {
  position: number;
  status: string;
  actual?: string;
  time_ms: number;
}

interface CandidateResult {
  status: string;
  compile_output?: string;
  public: CandidateTest[];
  hidden_passed: number;
  hidden_total: number;
}

interface TryCase {
  test_index: number;
  name?: string;
  class?: string;
  status: string;
  time_ms: number;
  expected: string;
  actual?: string;
  stderr?: string;
  actual_hash?: string;
}

interface TryResult {
  status: string;
  compile_output?: string;
  results: TryCase[];
}

interface SubmissionView {
  id: string;
  kind: string;
  status: string;
  result?: CandidateResult;
  score: number | null;
}

const SOURCE_SYNC_MS = 2000;
const POLL_MS = 1500;

// Highlighting is painted from the page's own palette, so the editor reads as
// part of the surface around it rather than as a widget with its own taste.
const highlightStyle = HighlightStyle.define([
  { tag: [tags.keyword, tags.controlKeyword, tags.moduleKeyword], color: "var(--color-accent-400)" },
  { tag: [tags.definitionKeyword, tags.operatorKeyword], color: "var(--color-accent-400)" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "var(--color-accent-2-400)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--color-accent-300)" },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: "var(--color-neutral-500)", fontStyle: "italic" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "var(--color-accent-2-300)" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: "var(--color-neutral-100)" },
  { tag: [tags.operator, tags.punctuation, tags.separator, tags.bracket], color: "var(--color-neutral-400)" },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--color-neutral-200)" },
  { tag: tags.invalid, color: "var(--color-accent-300)" },
]);

// Api talks to the attempt operations through /assess/api, the only path
// the sealed cookie is sent to. The HTML surface's CSRF check applies there,
// so every call carries the token from the page.
class Api {
  constructor(private readonly cfg: Config, private readonly onGone: () => void) {}

  // call addresses the attempt the island is working; callBase addresses the
  // API root, which is where try mode's problem endpoints live.
  private url(path: string): string {
    return this.cfg.api_base + "/attempts/" + this.cfg.attempt_id + path;
  }

  call<T>(method: string, path: string, body?: unknown): Promise<T> {
    return this.send<T>(method, this.url(path), body);
  }

  callBase<T>(method: string, path: string, body?: unknown): Promise<T> {
    return this.send<T>(method, this.cfg.api_base + path, body);
  }

  private async send<T>(method: string, url: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { "X-CSRF-Token": this.cfg.csrf };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetch(url, {
      method,
      credentials: "same-origin",
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (res.status === 410) {
      this.onGone();
      throw new Error("gone");
    }
    if (res.status === 409) {
      const st = await this.call<{ last_seq: number }>("GET", "");
      throw new SeqConflict(st.last_seq);
    }
    if (!res.ok) {
      let detail = res.statusText;
      try {
        detail = ((await res.json()) as { detail?: string }).detail ?? detail;
      } catch {
        // Not JSON; keep the status text.
      }
      if (res.status >= 400 && res.status < 500) throw new Rejected(res.status, detail);
      throw new Error(detail);
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }

  postEvents(events: AttemptEvent[]): Promise<number> {
    return this.call<{ last_seq: number }>("POST", "/events", { attempt_id: this.cfg.attempt_id, events }).then((r) => r.last_seq);
  }

  // beaconEvents posts the batch as a form so the CSRF field travels with
  // it; sendBeacon cannot set headers. Capped like a normal batch.
  beaconEvents(events: AttemptEvent[]): boolean {
    const form = new FormData();
    form.set("_csrf", this.cfg.csrf);
    form.set("batch", JSON.stringify({ attempt_id: this.cfg.attempt_id, events: events.slice(0, MAX_BATCH) }));
    return navigator.sendBeacon(this.cfg.beacon_url, form);
  }
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function formatRemaining(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const m = Math.floor(s / 60);
  return String(m).padStart(2, "0") + ":" + String(s % 60).padStart(2, "0");
}

// modKey names the modifier as this keyboard has it, so the shortcut the
// toolbar advertises is the one the candidate presses.
function modKey(): string {
  const ua = navigator.userAgent;
  return /Mac|iPhone|iPad/.test(ua) ? "⌘" : "Ctrl";
}

function mount(root: HTMLElement, cfg: Config): void {
  const mode: Mode = cfg.mode === "try" ? "try" : "attempt";
  const features = sessionFeatures(mode);
  const storage = (() => {
    try {
      return window.sessionStorage;
    } catch {
      return null;
    }
  })();
  const local = (() => {
    try {
      return window.localStorage;
    } catch {
      return null;
    }
  })();
  let closed = features.timer && cfg.status !== "started";
  const banner = el("p", "assess-banner");
  banner.hidden = true;
  root.appendChild(banner);

  const finish = (msg: string) => {
    if (closed) return;
    closed = true;
    banner.textContent = msg;
    banner.hidden = false;
    root.classList.add("assess-closed");
    void recorder.stop().then(() => window.setTimeout(() => window.location.reload(), 1500));
  };
  const api = new Api(cfg, () => finish("Time is up. Your last synced code has been submitted."));
  const recorder = new Recorder(cfg.attempt_id, { post: (e) => api.postEvents(e), beacon: (e) => api.beaconEvents(e) }, storage);
  const record = (type: EventType, problemID: string, data: unknown) => {
    if (features.recorder && !closed) recorder.record(type, problemID, data);
  };
  const paste = new PasteClassifier(storage, "assess:" + cfg.attempt_id + ":copied");
  // Integrity: only what the candidate agreed to on the consent screen. With
  // every measure off none of this exists and the session is what it was.
  const integrity = features.recorder ? cfg.integrity : undefined;
  const blockPaste = integrity?.block_paste === true;

  // Timer.
  if (features.timer) {
    const timer = el("div", "assess-timer");
    root.appendChild(timer);
    const due = new Date(cfg.expires_at);
    const tick = () => {
      const left = cfg.expires_at - Date.now();
      timer.textContent =
        cfg.format === "take_home"
          ? "Due " + due.toLocaleString(undefined, { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })
          : "Time remaining " + formatRemaining(left);
      if (left <= 0 && !closed) {
        window.clearInterval(timerId);
        // The server expires the attempt on the next request; ask it now.
        void api.call("GET", "").catch(() => undefined);
        finish("Time is up. Your last synced code has been submitted.");
      }
    };
    const timerId = window.setInterval(tick, 500);
    tick();
  }

  // Keymap: one choice for every editor on the page, remembered across
  // sessions so a candidate sets it once.
  let keymapName = readKeymap(local);
  const keymapConfs = new Map<string, Compartment>();
  // The control belongs beside the editor it changes, so it is moved into
  // whichever problem is on screen rather than sitting in a row of its own.
  const keymapLabel = el("label", "assess-keymap");
  keymapLabel.append(el("span", "assess-control-label", "Editor keys"));
  const keymapSelect = el("select");
  for (const name of KEYMAPS) {
    const opt = el("option", undefined, KEYMAP_LABELS[name]);
    opt.value = name;
    keymapSelect.appendChild(opt);
  }
  keymapSelect.value = keymapName;
  keymapLabel.appendChild(keymapSelect);
  const keymapSlots = new Map<string, HTMLElement>();

  const setKeymap = (name: KeymapName) => {
    keymapName = name;
    for (const [id, conf] of keymapConfs) {
      const v = views.get(id);
      if (v) applyKeymap(v.view, conf, name);
    }
    writeKeymap(local, name);
    if (current) record("keymap", current.id, { keymap: name });
  };
  keymapSelect.addEventListener("change", () => setKeymap(keymapSelect.value as KeymapName));

  // Layout: the problem tab strip above, the work area below it.
  const layout = el("div", "assess-layout");
  const tabs = el("nav", "assess-tabs");
  const work = el("div", "assess-work");
  layout.append(tabs, work);
  root.appendChild(layout);

  const views = new Map<string, { view: EditorView; language: string; langConf: Compartment; results: HTMLElement }>();
  let current: Problem | null = null;

  const syncTimers = new Map<string, number>();
  const scheduleSync = (p: Problem) => {
    if (!features.recorder) return;
    const existing = syncTimers.get(p.id);
    if (existing !== undefined) window.clearTimeout(existing);
    syncTimers.set(
      p.id,
      window.setTimeout(() => {
        const v = views.get(p.id);
        if (!v || closed) return;
        void api.call("PUT", "/problems/" + p.id + "/source", { language: v.language, source: v.view.state.doc.toString() }).catch(() => undefined);
      }, SOURCE_SYNC_MS),
    );
  };

  const poll = async (p: Problem, id: string, results: HTMLElement) => {
    results.textContent = "Running…";
    for (;;) {
      await new Promise((r) => window.setTimeout(r, POLL_MS));
      if (closed) return;
      let sub: SubmissionView;
      try {
        sub = await api.call<SubmissionView>("GET", "/submissions/" + id);
      } catch (err) {
        results.textContent = "Could not read the result: " + (err as Error).message;
        return;
      }
      if (sub.status === "done" || sub.status === "error") {
        results.textContent = "";
        const head = el("p", undefined, (sub.kind === "run" ? "Run" : "Submission") + " " + sub.status + (sub.score !== null ? " — score " + sub.score : ""));
        results.appendChild(head);
        if (sub.result) {
          if (sub.result.compile_output) results.appendChild(el("pre", undefined, sub.result.compile_output));
          for (const t of sub.result.public) {
            const line = el("div", "assess-case", "Example " + t.position + ": " + t.status + " (" + t.time_ms + " ms)");
            if (t.actual) line.appendChild(el("pre", undefined, t.actual));
            results.appendChild(line);
          }
          if (sub.result.hidden_total > 0) {
            results.appendChild(el("p", undefined, "Hidden cases: " + sub.result.hidden_passed + " of " + sub.result.hidden_total + " passed"));
          }
        }
        return;
      }
      results.textContent = sub.kind === "run" ? "Running… (" + sub.status + ")" : "Submitting… (" + sub.status + ")";
    }
  };

  const buildProblem = (p: Problem): HTMLElement => {
    const pane = el("section", "assess-problem");
    pane.hidden = true;
    // The split is the shape of the work: what to build on the left, the
    // building of it on the right, each column scrolling on its own.
    const split = el("div", "assess-split");
    const brief = el("div", "assess-brief");
    brief.append(el("h2", undefined, p.title));
    const statement = el("div", "assess-statement prose");
    // The server sends the statement as HTML it rendered and sanitised; the
    // plain text is the fallback for a boot config written before that field.
    if (p.statement_html) statement.innerHTML = p.statement_html;
    else statement.textContent = p.statement;
    brief.appendChild(statement);
    if (p.sql_schema) {
      const schema = el("details");
      schema.open = true;
      schema.append(el("summary", undefined, "Schema"), el("pre", undefined, p.sql_schema));
      brief.appendChild(schema);
    }

    const solve = el("div", "assess-solve");
    const bar = el("div", "assess-bar");
    const select = el("select");
    for (const lang of p.languages) {
      const opt = el("option", undefined, languageLabel(lang));
      opt.value = lang;
      select.appendChild(opt);
    }
    const initialLang = p.language && p.languages.includes(p.language) ? p.language : p.languages[0] ?? "";
    select.value = initialLang;
    const runBtn = el("button", undefined, "Run example cases");
    bar.append(select, runBtn);
    const submitBtn = el("button", "primary", "Submit");
    if (features.submit) bar.appendChild(submitBtn);
    // The keymap control lands here when this problem is the one on screen.
    const keymapSlot = el("div", "assess-keymap-slot");
    keymapSlots.set(p.id, keymapSlot);
    bar.appendChild(keymapSlot);
    // The shortcuts stay on screen because vim and emacs both claim keys a
    // candidate might otherwise guess at.
    const shortcuts = features.submit ? modKey() + "-Enter runs · " + modKey() + "-Shift-Enter submits" : modKey() + "-Enter runs";
    bar.appendChild(el("span", "assess-shortcuts", shortcuts));
    solve.appendChild(bar);

    const editorHost = el("div", "assess-editor");
    solve.appendChild(editorHost);
    // Below the editor, the two things a run is judged against: the example
    // cases and whatever the last run said about them.
    const console_ = el("div", "assess-console");
    if (p.public_tests.length > 0) {
      const tests = el("details", "assess-tests");
      tests.open = true;
      tests.appendChild(el("summary", undefined, "Example cases"));
      for (const t of p.public_tests) {
        const row = el("div", "assess-test");
        row.append(el("pre", undefined, "input:\n" + t.input), el("pre", undefined, "expected:\n" + t.expected));
        tests.appendChild(row);
      }
      console_.appendChild(tests);
    }
    const results = el("div", "assess-results");
    console_.appendChild(results);
    solve.appendChild(console_);

    split.append(brief, solve);
    pane.appendChild(split);

    const langConf = new Compartment();
    const keymapConf = new Compartment();
    keymapConfs.set(p.id, keymapConf);
    const view = new EditorView({
      state: EditorState.create({
        doc: p.source,
        extensions: [
          // Run and Submit are bound above every keymap so neither vim nor
          // emacs can shadow the only two actions that reach the server.
          Prec.highest(
            keymap.of([
              { key: "Mod-Enter", preventDefault: true, run: () => (void execute("run"), true) },
              { key: "Mod-Shift-Enter", preventDefault: true, run: () => (features.submit ? (void execute("submit"), true) : false) },
            ]),
          ),
          keymapConf.of(keymapExtension(keymapName)),
          lineNumbers(),
          highlightActiveLine(),
          drawSelection(),
          history(),
          syntaxHighlighting(highlightStyle),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          langConf.of(languageSupport(initialLang)),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged || closed) return;
            record("edit", p.id, u.changes.toJSON());
            scheduleSync(p);
          }),
          EditorView.domEventHandlers({
            copy: (_e, v) => void paste.copied(selectedText(v)),
            cut: (_e, v) => void paste.copied(selectedText(v)),
            paste: (e) => {
              if (blockPaste) {
                e.preventDefault();
                record("paste", p.id, blockedPaste());
                return true;
              }
              const text = e.clipboardData?.getData("text/plain") ?? "";
              void paste.classify(text).then((rec) => record("paste", p.id, rec));
              return false;
            },
          }),
        ],
      }),
      parent: editorHost,
    });
    const entry = { view, language: initialLang, langConf, results };
    views.set(p.id, entry);

    select.addEventListener("change", () => {
      entry.language = select.value;
      view.dispatch({ effects: langConf.reconfigure(languageSupport(select.value)) });
      record("lang_change", p.id, { language: select.value });
      scheduleSync(p);
    });

    const execute = async (kind: "run" | "submit") => {
      if (closed) return;
      runBtn.disabled = submitBtn.disabled = true;
      const path = executePath(mode, cfg.attempt_id, p.id, kind);
      const payload = { language: entry.language, source: view.state.doc.toString() };
      try {
        if (mode === "try") {
          results.textContent = "Running…";
          renderTry(await api.callBase<TryResult>("POST", path, payload), results);
          return;
        }
        // executePath already carries "/attempts/{id}", so this goes through
        // callBase; api.call would prefix the attempt a second time.
        const res = await api.callBase<{ submission_id: string }>("POST", path, payload);
        record(kind, p.id, { submission_id: res.submission_id });
        await poll(p, res.submission_id, results);
      } catch (err) {
        if ((err as Error).message !== "gone") results.textContent = "Could not " + kind + ": " + (err as Error).message;
      } finally {
        runBtn.disabled = submitBtn.disabled = false;
      }
    };
    runBtn.addEventListener("click", () => void execute("run"));
    submitBtn.addEventListener("click", () => void execute("submit"));
    return pane;
  };

  const panes = new Map<string, HTMLElement>();
  for (const p of cfg.problems) {
    const pane = buildProblem(p);
    panes.set(p.id, pane);
    work.appendChild(pane);
    const tab = el("button", "assess-tab", p.title);
    tab.addEventListener("click", () => show(p));
    tabs.appendChild(tab);
  }
  if (cfg.problems.length < 2) tabs.hidden = true;
  const show = (p: Problem) => {
    current = p;
    for (const [id, pane] of panes) pane.hidden = id !== p.id;
    keymapSlots.get(p.id)?.appendChild(keymapLabel);
    Array.from(tabs.children).forEach((t, i) => t.classList.toggle("active", cfg.problems[i].id === p.id));
    views.get(p.id)?.view.focus();
  };
  if (cfg.problems.length > 0) show(cfg.problems[0]);

  if (features.submit) {
    const done = el("div", "assess-finish");
    const finishBtn = el("button", "danger", "Finish assessment");
    finishBtn.addEventListener("click", () => {
      if (!window.confirm("Finish now? Unsubmitted editors are submitted as they are.")) return;
      void api
        .call("POST", "/finish")
        .then(() => finish("Assessment submitted. Thank you."))
        .catch((err) => {
          if ((err as Error).message !== "gone") window.alert("Could not finish: " + (err as Error).message);
        });
    });
    done.appendChild(finishBtn);
    root.appendChild(done);
  }

  if (features.recorder) {
    window.addEventListener("focus", () => current && !closed && record("focus", current.id, {}));
    window.addEventListener("blur", () => current && !closed && record("blur", current.id, {}));
    recorder.start();
    // The stored choice is part of the recording: the replay has to know
    // which keymap produced the changesets that follow.
    if (current) record("keymap", (current as Problem).id, { keymap: keymapName });
    if (integrity?.fullscreen) watchFullscreen(root, () => current, record);
    if (integrity?.webcam) takeSnapshots(cfg, integrity, api, recorder, () => current, record);
  }
}


// watchFullscreen asks for fullscreen when the assessment requires it and
// keeps the recording honest about every time the candidate leaves. Leaving
// pauses nothing: the banner is a reminder, not a wall.
function watchFullscreen(root: HTMLElement, currentProblem: () => Problem | null, record: (type: EventType, problemID: string, data: unknown) => void): void {
  const alert = el("div", "assess-alert");
  const text = el("p", "assess-alert-text", "This assessment runs in fullscreen.");
  const back = el("button", "assess-alert-action", "Go fullscreen");
  alert.append(text, back);
  alert.hidden = true;
  root.prepend(alert);

  const request = () => {
    const target = document.documentElement;
    const req = target.requestFullscreen ? target.requestFullscreen() : Promise.reject(new Error("no fullscreen here"));
    void req.catch(() => {
      // Fullscreen needs a gesture the page load did not have; ask for one.
      text.textContent = "This assessment runs in fullscreen.";
      alert.hidden = false;
    });
  };
  back.addEventListener("click", request);
  document.addEventListener("fullscreenchange", () => {
    const on = document.fullscreenElement !== null;
    const p = currentProblem();
    if (p) record(on ? "fullscreen_enter" : "fullscreen_exit", p.id, {});
    text.textContent = on ? "This assessment runs in fullscreen." : "You left fullscreen. Your work continues, and the recording notes it.";
    alert.hidden = on;
  });
  request();
}

// takeSnapshots runs the webcam beat for the length of the session. The
// endpoint appends the snapshot event itself, so a stored frame is followed
// by a resync of the recorder's counter; a beat that produced nothing is
// recorded here as the gap it is.
function takeSnapshots(
  cfg: Config,
  integrity: IntegrityConfig,
  api: Api,
  recorder: Recorder,
  currentProblem: () => Problem | null,
  record: (type: EventType, problemID: string, data: unknown) => void,
): void {
  let camera: Camera | null = null;
  const loop = new SnapshotLoop(integrity.webcam_interval_s, {
    capture: async () => {
      // Permission was granted on the consent screen, so this opens the
      // device without asking the candidate again.
      if (!camera) camera = await openCamera();
      return camera.frame();
    },
    upload: async (seq, frame) => {
      await uploadFrame(integrity.snapshot_url, cfg.csrf, frame, seq);
      const st = await api.call<{ last_seq: number }>("GET", "");
      recorder.resync(st.last_seq);
    },
    missed: (seq) => {
      const p = currentProblem();
      if (p) record("snapshot", p.id, { seq, ok: false });
    },
  });
  loop.start();
  window.addEventListener("pagehide", () => {
    loop.stop();
    camera?.stop();
  });
}

// renderTry draws a try run's verdict. The try endpoint answers in one call —
// there is no submission to poll for, because none was written. A case that
// did not pass opens on what the program printed beside what was wanted,
// because that comparison is the whole of debugging it.
function renderTry(res: TryResult, into: HTMLElement): void {
  into.textContent = "";
  const passed = res.results.filter((c) => c.status === "pass").length;
  into.appendChild(el("p", undefined, res.status === "ok" ? passed + " of " + res.results.length + " example cases passed" : "The run ended in " + res.status.replace("_", " ")));
  if (res.compile_output) into.appendChild(el("pre", undefined, res.compile_output));
  for (const c of res.results) {
    const label = c.name ? c.name : "Example " + (c.test_index + 1);
    const row = el("details", "assess-case");
    row.open = c.status !== "pass";
    row.appendChild(el("summary", undefined, label + ": " + c.status + " (" + c.time_ms + " ms)"));
    const output = el("div", "assess-output");
    output.append(outputPane("your output", c.actual ?? ""), outputPane("expected", c.expected));
    row.appendChild(output);
    if (c.stderr) row.appendChild(outputPane("stderr", c.stderr));
    into.appendChild(row);
  }
  // The verdict sits under the example cases, so bring it into view rather
  // than leaving the reader to hunt for the answer they just asked for.
  into.scrollIntoView({ block: "nearest" });
}

// outputPane is one labelled block of program output. An empty one says so
// rather than showing nothing, because "printed nothing" is itself the answer
// to most of the runs that fail.
function outputPane(label: string, text: string): HTMLElement {
  const pane = el("div", "assess-pane");
  pane.appendChild(el("span", "assess-pane-label", label));
  pane.appendChild(el("pre", undefined, text === "" ? "(nothing)" : text));
  return pane;
}

function selectedText(v: EditorView): string {
  return v.state.selection.ranges.map((r) => v.state.sliceDoc(r.from, r.to)).join("\n");
}

declare global {
  interface Window {
    RecruitingEditor: { mount: (el: HTMLElement, cfg: Config) => void };
  }
}

window.RecruitingEditor = { mount };

const cfgEl = document.getElementById("assess-config");
const rootEl = document.getElementById("assess");
if (cfgEl) {
  const booted = JSON.parse(cfgEl.textContent ?? "{}") as Config;
  if (rootEl) mount(rootEl, booted);
  else mountConsent(booted);
}
