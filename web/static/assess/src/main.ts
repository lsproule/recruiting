// Candidate assessment island: one CodeMirror editor per problem, a
// server-enforced countdown, run/submit against the API, and the event
// recording the integrity signals and replay are built on.

import { EditorState, Compartment } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { python } from "@codemirror/lang-python";
import { javascript } from "@codemirror/lang-javascript";
import { sql } from "@codemirror/lang-sql";
import { Recorder, SeqConflict, Rejected, MAX_BATCH, type AttemptEvent } from "./recorder";
import { PasteClassifier } from "./paste";

interface TestCase {
  input: string;
  expected: string;
}

interface Problem {
  id: string;
  title: string;
  kind: string;
  statement: string;
  languages: string[];
  language: string;
  source: string;
  sql_schema: string;
  public_tests: TestCase[];
}

interface Config {
  attempt_id: string;
  api_base: string;
  beacon_url: string;
  csrf: string;
  status: string;
  expires_at: number; // unix millis
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

interface SubmissionView {
  id: string;
  kind: string;
  status: string;
  result?: CandidateResult;
  score: number | null;
}

const SOURCE_SYNC_MS = 2000;
const POLL_MS = 1500;

function languageSupport(lang: string) {
  switch (lang) {
    case "python":
      return python();
    case "node":
      return javascript();
    case "sql":
      return sql();
    default:
      return [];
  }
}

// Api talks to the attempt operations through /assess/api, the only path
// the sealed cookie is sent to. The HTML surface's CSRF check applies there,
// so every call carries the token from the page.
class Api {
  constructor(private readonly cfg: Config, private readonly onGone: () => void) {}

  private url(path: string): string {
    return this.cfg.api_base + "/attempts/" + this.cfg.attempt_id + path;
  }

  async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { "X-CSRF-Token": this.cfg.csrf };
    if (body !== undefined) headers["Content-Type"] = "application/json";
    const res = await fetch(this.url(path), {
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

function mount(root: HTMLElement, cfg: Config): void {
  const storage = (() => {
    try {
      return window.sessionStorage;
    } catch {
      return null;
    }
  })();
  let closed = cfg.status !== "started";
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
  const paste = new PasteClassifier(storage, "assess:" + cfg.attempt_id + ":copied");

  // Timer.
  const timer = el("div", "assess-timer");
  root.appendChild(timer);
  const tick = () => {
    const left = cfg.expires_at - Date.now();
    timer.textContent = "Time remaining " + formatRemaining(left);
    if (left <= 0 && !closed) {
      window.clearInterval(timerId);
      // The server expires the attempt on the next request; ask it now.
      void api.call("GET", "").catch(() => undefined);
      finish("Time is up. Your last synced code has been submitted.");
    }
  };
  const timerId = window.setInterval(tick, 500);
  tick();

  // Layout: problem tabs on the left, work area on the right.
  const layout = el("div", "assess-layout");
  const tabs = el("nav", "assess-tabs");
  const work = el("div", "assess-work");
  layout.append(tabs, work);
  root.appendChild(layout);

  const views = new Map<string, { view: EditorView; language: string; langConf: Compartment; results: HTMLElement }>();
  let current: Problem | null = null;

  const syncTimers = new Map<string, number>();
  const scheduleSync = (p: Problem) => {
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
    pane.append(el("h2", undefined, p.title));
    const statement = el("div", "assess-statement");
    statement.textContent = p.statement;
    pane.appendChild(statement);
    if (p.sql_schema) {
      const schema = el("details");
      schema.append(el("summary", undefined, "Schema"), el("pre", undefined, p.sql_schema));
      pane.appendChild(schema);
    }
    if (p.public_tests.length > 0) {
      const tests = el("details", "assess-tests");
      tests.open = true;
      tests.appendChild(el("summary", undefined, "Example cases"));
      for (const t of p.public_tests) {
        const row = el("div", "assess-test");
        row.append(el("pre", undefined, "input:\n" + t.input), el("pre", undefined, "expected:\n" + t.expected));
        tests.appendChild(row);
      }
      pane.appendChild(tests);
    }

    const bar = el("div", "assess-bar");
    const select = el("select");
    for (const lang of p.languages) {
      const opt = el("option", undefined, lang);
      opt.value = lang;
      select.appendChild(opt);
    }
    const initialLang = p.language && p.languages.includes(p.language) ? p.language : p.languages[0] ?? "";
    select.value = initialLang;
    const runBtn = el("button", undefined, "Run example cases");
    const submitBtn = el("button", "primary", "Submit");
    bar.append(select, runBtn, submitBtn);
    pane.appendChild(bar);

    const editorHost = el("div", "assess-editor");
    pane.appendChild(editorHost);
    const results = el("div", "assess-results");
    pane.appendChild(results);

    const langConf = new Compartment();
    const view = new EditorView({
      state: EditorState.create({
        doc: p.source,
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          drawSelection(),
          history(),
          keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
          langConf.of(languageSupport(initialLang)),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged || closed) return;
            recorder.record("edit", p.id, u.changes.toJSON());
            scheduleSync(p);
          }),
          EditorView.domEventHandlers({
            copy: (_e, v) => void paste.copied(selectedText(v)),
            cut: (_e, v) => void paste.copied(selectedText(v)),
            paste: (e) => {
              const text = e.clipboardData?.getData("text/plain") ?? "";
              void paste.classify(text).then((rec) => recorder.record("paste", p.id, rec));
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
      recorder.record("lang_change", p.id, { language: select.value });
      scheduleSync(p);
    });

    const execute = async (kind: "run" | "submit") => {
      if (closed) return;
      runBtn.disabled = submitBtn.disabled = true;
      try {
        const res = await api.call<{ submission_id: string }>("POST", "/problems/" + p.id + "/" + kind, {
          language: entry.language,
          source: view.state.doc.toString(),
        });
        recorder.record(kind, p.id, { submission_id: res.submission_id });
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
  const show = (p: Problem) => {
    current = p;
    for (const [id, pane] of panes) pane.hidden = id !== p.id;
    Array.from(tabs.children).forEach((t, i) => t.classList.toggle("active", cfg.problems[i].id === p.id));
    views.get(p.id)?.view.focus();
  };
  if (cfg.problems.length > 0) show(cfg.problems[0]);

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

  window.addEventListener("focus", () => current && !closed && recorder.record("focus", current.id, {}));
  window.addEventListener("blur", () => current && !closed && recorder.record("blur", current.id, {}));
  recorder.start();
}

function selectedText(v: EditorView): string {
  return v.state.selection.ranges.map((r) => v.state.sliceDoc(r.from, r.to)).join("\n");
}

const cfgEl = document.getElementById("assess-config");
const rootEl = document.getElementById("assess");
if (cfgEl && rootEl) {
  mount(rootEl, JSON.parse(cfgEl.textContent ?? "{}") as Config);
}
