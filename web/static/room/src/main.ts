// The live room island and the sprint console. A room is a grid of video
// tiles, a shared CodeMirror editor, and a run pane; the console is the
// sprint clock that puts an interviewer or a candidate into the right room
// at the right minute and asks the interviewer for a rating after.

import { EditorState, Compartment, Prec } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, drawSelection } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags } from "@lezer/highlight";

import { languageLabel, languageSupport } from "../../assess/src/languages";
import { KEYMAPS, KEYMAP_LABELS, applyKeymap, keymapExtension, readKeymap, writeKeymap, type KeymapName } from "../../assess/src/keymap";
import { Channel, type Hello, type Peer, type RoomEvent } from "./signal";
import { Mesh } from "./rtc";
import { SharedDoc } from "./doc";
import { clockOffset, formatCountdown, headline, lastEnded, nextMine, type RoundEntry, type SprintState } from "./clock";

interface RoomConfig {
  key: string;
  base: string;
  csrf: string;
  name: string;
  role: string;
  title: string;
  subtitle: string;
  language: string;
  source: string;
  languages: string[];
  ice: RTCIceServer[];
  run: boolean;
  closes_at: number;
  pairing?: string;
}

interface ConsoleConfig {
  state_url: string;
  csrf: string;
  ice: RTCIceServer[];
  role: string;
  interviewer: boolean;
  name?: string;
  summary_url?: string;
}

interface RunResult {
  status: string;
  compile_output: string;
  stdout: string;
  stderr: string;
  time_ms: number;
}

const CODE_SAVE_MS = 1500;
const STATE_POLL_MS = 2000;

const highlightStyle = HighlightStyle.define([
  { tag: [tags.keyword, tags.controlKeyword, tags.moduleKeyword, tags.definitionKeyword, tags.operatorKeyword], color: "var(--color-accent-400)" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "var(--color-accent-2-400)" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "var(--color-accent-300)" },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: "var(--color-neutral-500)", fontStyle: "italic" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "var(--color-accent-2-300)" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: "var(--color-neutral-100)" },
  { tag: [tags.operator, tags.punctuation, tags.separator, tags.bracket], color: "var(--color-neutral-400)" },
]);

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function readConfig<T>(root: HTMLElement): T | null {
  const id = root.dataset.config;
  const node = id ? document.getElementById(id) : null;
  if (!node) return null;
  try {
    return JSON.parse(node.textContent ?? "") as T;
  } catch {
    return null;
  }
}

export interface RoomHandle {
  leave(): void;
}

// mountRoom builds the room inside root and joins it.
export function mountRoom(root: HTMLElement, cfg: RoomConfig): RoomHandle {
  root.textContent = "";
  const local = (() => {
    try {
      return window.localStorage;
    } catch {
      return null;
    }
  })();

  // ── Media column ────────────────────────────────────────────────────
  const media = el("div", "room-media");
  const grid = el("div", "room-grid");
  const controls = el("div", "room-controls");
  const note = el("p", "room-note", "Connecting…");
  media.append(grid, controls, note);

  const tiles = new Map<string, HTMLElement>();
  const tile = (id: string, label: string, kind: "self" | "camera" | "screen", stream: MediaStream | null): HTMLElement => {
    const existing = tiles.get(id);
    if (existing) return existing;
    const t = el("div", "room-tile room-tile-" + kind);
    t.dataset.tile = id;
    const video = el("video");
    video.autoplay = true;
    video.playsInline = true;
    if (kind === "self") video.muted = true;
    if (stream) video.srcObject = stream;
    t.appendChild(video);
    if (!stream) t.appendChild(el("div", "room-tile-status", "connecting…"));
    t.appendChild(el("span", "room-tile-label", label));
    tiles.set(id, t);
    if (kind === "screen") grid.prepend(t);
    else grid.appendChild(t);
    return t;
  };
  const setTileStream = (id: string, stream: MediaStream) => {
    const t = tiles.get(id);
    if (!t) return;
    const video = t.querySelector("video");
    if (video) video.srcObject = stream;
    t.querySelector(".room-tile-status")?.remove();
  };
  const dropTile = (id: string) => {
    tiles.get(id)?.remove();
    tiles.delete(id);
  };

  const peers = new Map<string, Peer>();
  let self = "";
  let localStream: MediaStream | null = null;

  const channel = new Channel(cfg.base, cfg.csrf);
  const mesh = new Mesh("", cfg.ice, () => undefined, {
    onTrack: () => undefined,
    onStreamEnded: () => undefined,
    onState: () => undefined,
  });
  // The mesh needs the self id from hello; it is rebuilt then. Until then
  // nothing is negotiated.
  let live: Mesh | null = null;

  const doc = new SharedDoc(channel, () => scheduleSave());

  // ── Editor column ───────────────────────────────────────────────────
  const editorCol = el("div", "room-editor");
  const bar = el("div", "room-bar");
  const langSelect = el("select");
  langSelect.setAttribute("aria-label", "Language");
  for (const id of cfg.languages) {
    const opt = el("option", undefined, languageLabel(id));
    opt.value = id;
    langSelect.appendChild(opt);
  }
  let language = cfg.languages.includes(cfg.language) ? cfg.language : cfg.languages[0];
  langSelect.value = language;
  const keymapSelect = el("select");
  keymapSelect.setAttribute("aria-label", "Editor keys");
  for (const name of KEYMAPS) {
    const opt = el("option", undefined, KEYMAP_LABELS[name]);
    opt.value = name;
    keymapSelect.appendChild(opt);
  }
  let keymapName: KeymapName = readKeymap(local);
  keymapSelect.value = keymapName;
  const runButton = el("button", "btn btn-primary", "Run");
  runButton.type = "button";
  runButton.disabled = !cfg.run;
  const saved = el("span", "room-saved", "");
  bar.append(langSelect, keymapSelect, runButton, saved);

  const editorHost = el("div", "assess-editor");
  const stdin = el("textarea", "room-stdin");
  stdin.placeholder = "stdin for the next run";
  stdin.setAttribute("aria-label", "Standard input");
  const output = el("div", "room-output");
  const outPane = el("div", "assess-pane");
  outPane.append(el("div", "assess-pane-label", "stdout"));
  const outPre = el("pre", "room-stdout", "");
  outPane.appendChild(outPre);
  const errPane = el("div", "assess-pane");
  errPane.append(el("div", "assess-pane-label", "stderr"));
  const errPre = el("pre", "room-stderr", "");
  errPane.appendChild(errPre);
  output.append(outPane, errPane);
  editorCol.append(bar, editorHost, stdin, output);

  root.append(media, editorCol);

  const langConf = new Compartment();
  const keymapConf = new Compartment();
  let view: EditorView | null = null;
  const mountEditor = () => {
    if (view) return;
    view = new EditorView({
      parent: editorHost,
      state: EditorState.create({
        doc: doc.text(),
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          drawSelection(),
          history(),
          syntaxHighlighting(highlightStyle),
          keymapConf.of(keymapExtension(keymapName)),
          Prec.high(keymap.of([indentWithTab])),
          keymap.of([...defaultKeymap, ...historyKeymap]),
          langConf.of(languageSupport(language)),
          doc.extension(),
        ],
      }),
    });
  };

  let saveTimer = 0;
  const scheduleSave = () => {
    window.clearTimeout(saveTimer);
    saveTimer = window.setTimeout(() => {
      void channel
        .post("/code", { from: self, language, source: doc.text() })
        .then(() => {
          saved.textContent = "Saved";
        })
        .catch(() => {
          saved.textContent = "Not saved";
        });
    }, CODE_SAVE_MS);
  };

  const setLanguage = (id: string, announce: boolean) => {
    language = id;
    langSelect.value = id;
    view?.dispatch({ effects: langConf.reconfigure(languageSupport(id)) });
    if (announce) scheduleSave();
  };
  langSelect.addEventListener("change", () => setLanguage(langSelect.value, true));
  keymapSelect.addEventListener("change", () => {
    keymapName = keymapSelect.value as KeymapName;
    if (view) applyKeymap(view, keymapConf, keymapName);
    writeKeymap(local, keymapName);
  });

  runButton.addEventListener("click", async () => {
    runButton.disabled = true;
    outPre.textContent = "Running…";
    errPre.textContent = "";
    try {
      const res = await channel.post<RunResult>("/run", { language, source: doc.text(), stdin: stdin.value });
      outPre.textContent = res.stdout || (res.status === "ok" ? "(no output)" : "");
      const parts: string[] = [];
      if (res.compile_output) parts.push(res.compile_output);
      if (res.stderr) parts.push(res.stderr);
      if (res.status !== "ok") parts.push("status: " + res.status);
      parts.push(res.time_ms + " ms");
      errPre.textContent = parts.join("\n");
    } catch (err) {
      outPre.textContent = "";
      errPre.textContent = "Run failed: " + (err as Error).message;
    } finally {
      runButton.disabled = !cfg.run;
    }
  });

  // ── Controls ────────────────────────────────────────────────────────
  const micButton = el("button", "btn btn-secondary on", "Mute");
  micButton.type = "button";
  const camButton = el("button", "btn btn-secondary on", "Camera off");
  camButton.type = "button";
  const screenButton = el("button", "btn btn-secondary", "Share screen");
  screenButton.type = "button";
  controls.append(micButton, camButton, screenButton);

  let micOn = true;
  let camOn = true;
  micButton.addEventListener("click", () => {
    micOn = !micOn;
    localStream?.getAudioTracks().forEach((t) => (t.enabled = micOn));
    micButton.textContent = micOn ? "Mute" : "Unmute";
    micButton.classList.toggle("on", micOn);
  });
  camButton.addEventListener("click", () => {
    camOn = !camOn;
    localStream?.getVideoTracks().forEach((t) => (t.enabled = camOn));
    camButton.textContent = camOn ? "Camera off" : "Camera on";
    camButton.classList.toggle("on", camOn);
  });
  let screenStream: MediaStream | null = null;
  screenButton.addEventListener("click", async () => {
    if (screenStream) {
      live?.stopScreen();
      screenStream = null;
      dropTile("self-screen");
      screenButton.textContent = "Share screen";
      screenButton.classList.remove("on");
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getDisplayMedia({ video: true, audio: false });
      screenStream = stream;
      tile("self-screen", "Your screen", "screen", stream);
      live?.shareScreen(stream);
      screenButton.textContent = "Stop sharing";
      screenButton.classList.add("on");
      stream.getVideoTracks()[0]?.addEventListener("ended", () => {
        if (screenStream === stream) screenButton.click();
      });
    } catch (err) {
      note.textContent = "Screen sharing was refused: " + (err as Error).message;
    }
  });

  // ── Joining ─────────────────────────────────────────────────────────
  const startMedia = async () => {
    try {
      localStream = await navigator.mediaDevices.getUserMedia({ video: true, audio: true });
    } catch {
      try {
        localStream = await navigator.mediaDevices.getUserMedia({ audio: true });
      } catch {
        localStream = null;
      }
    }
    tile("self", cfg.name + " (you)", "self", localStream);
    if (!localStream) note.textContent = "No camera or microphone; you can still see the others and share the editor.";
    live?.setLocalStream(localStream);
  };

  const buildMesh = (selfId: string) => {
    live?.close();
    live = new Mesh(
      selfId,
      cfg.ice,
      (to, data) => void channel.post("/signal", { from: selfId, to, data }).catch(() => undefined),
      {
        onTrack: ({ peer, stream, kind }) => {
          const who = peers.get(peer);
          const id = kind === "screen" ? peer + ":screen:" + stream.id : peer;
          const label = kind === "screen" ? (who?.name ?? "Someone") + "'s screen" : `${who?.name ?? "Someone"} · ${who?.role ?? ""}`;
          tile(id, label, kind, stream);
          setTileStream(id, stream);
        },
        onStreamEnded: (peer, streamId) => {
          dropTile(peer + ":screen:" + streamId);
        },
        onState: (peer, state) => {
          const t = tiles.get(peer);
          if (!t) return;
          if (state === "connected") t.querySelector(".room-tile-status")?.remove();
          else if (state === "failed" || state === "disconnected") {
            if (!t.querySelector(".room-tile-status")) t.appendChild(el("div", "room-tile-status", state));
          }
        },
      },
    );
    live.setLocalStream(localStream);
    if (screenStream) live.shareScreen(screenStream);
  };

  let started = false;
  channel.on((ev: RoomEvent) => {
    switch (ev.type) {
      case "hello": {
        const hello = ev.data as Hello;
        const reconnect = self !== "";
        self = hello.self;
        peers.clear();
        for (const p of hello.peers) peers.set(p.id, p);
        if (!reconnect) {
          doc.begin(self, cfg.name, cfg.role, hello.doc, hello.source);
          setLanguage(hello.language && cfg.languages.includes(hello.language) ? hello.language : language, false);
          mountEditor();
        } else {
          doc.reconnected(self, hello.doc);
        }
        for (const id of [...tiles.keys()]) if (id !== "self" && id !== "self-screen") dropTile(id);
        buildMesh(self);
        for (const p of hello.peers) {
          tile(p.id, `${p.name} · ${p.role}`, "camera", null);
          live?.addPeer(p.id);
        }
        note.textContent = hello.peers.length === 0 ? "You are the first one here." : "";
        started = true;
        root.dataset.state = "joined";
        break;
      }
      case "peer-joined": {
        const p = ev.data as Peer;
        peers.set(p.id, p);
        tile(p.id, `${p.name} · ${p.role}`, "camera", null);
        live?.addPeer(p.id);
        note.textContent = "";
        break;
      }
      case "peer-left": {
        const p = ev.data as Peer;
        peers.delete(p.id);
        live?.removePeer(p.id);
        dropTile(p.id);
        for (const id of [...tiles.keys()]) if (id.startsWith(p.id + ":screen:")) dropTile(id);
        break;
      }
      case "signal": {
        const data = ev.data as { type?: string; update?: string } | undefined;
        if (data?.type === "awareness" && data.update) {
          doc.applyAwareness(data.update);
        } else if (ev.from) {
          void live?.handle(ev.from, ev.data);
        }
        break;
      }
      case "doc":
        doc.applyRemote(ev.data as string);
        break;
      case "code": {
        const data = ev.data as { language?: string };
        if (data.language && data.language !== language && cfg.languages.includes(data.language)) setLanguage(data.language, false);
        break;
      }
      case "disconnected":
        note.textContent = "Connection lost; reconnecting…";
        break;
    }
  });

  void startMedia().then(() => channel.connect());

  const leave = () => {
    if (!started && !localStream) return;
    channel.close();
    live?.close();
    localStream?.getTracks().forEach((t) => t.stop());
    screenStream?.getTracks().forEach((t) => t.stop());
    doc.destroy();
    view?.destroy();
  };
  window.addEventListener("pagehide", leave, { once: true });
  return { leave };
}

// mountConsole runs the sprint clock for one person: the interviewer's
// console or the candidate's lobby. Rooms open in a frame so each
// conversation is a fresh room, and the rating card is asked for as soon
// as a conversation ends.
export function mountConsole(root: HTMLElement, cfg: ConsoleConfig): void {
  const stage = root.querySelector<HTMLElement>("#console-stage") ?? root;
  const rating = root.querySelector<HTMLElement>("#console-rating");
  const clockLine = document.getElementById("console-clock");
  const rounds = root.querySelectorAll<HTMLElement>("#console-rounds li");

  let state: SprintState | null = null;
  let offset = 0;
  let shown: string | null = null; // pairing whose room is on screen
  let askedFor: string | null = null; // pairing whose rating card was loaded

  const serverNow = () => Date.now() - offset;

  const showCard = (title: string, body: string, big?: string) => {
    stage.textContent = "";
    const card = el("section", "panel empty-card console-break");
    card.appendChild(el("h2", undefined, title));
    if (big !== undefined) card.appendChild(el("div", "console-countdown num", big));
    card.appendChild(el("p", "muted", body));
    stage.appendChild(card);
  };

  const showRoom = (entry: RoundEntry) => {
    if (shown === entry.pairing) return;
    shown = entry.pairing;
    stage.textContent = "";
    const frame = el("iframe", "console-frame");
    frame.src = entry.room + "?embed=1";
    frame.allow = "camera; microphone; display-capture; autoplay";
    frame.setAttribute("title", "Room with " + entry.with);
    frame.dataset.pairing = entry.pairing;
    stage.appendChild(frame);
  };

  const askRating = (entry: RoundEntry) => {
    if (!rating || !cfg.interviewer || !entry.rate || entry.rated || askedFor === entry.pairing) return;
    askedFor = entry.pairing;
    const htmx = (window as unknown as { htmx?: { ajax: (m: string, u: string, o: unknown) => void } }).htmx;
    if (htmx) htmx.ajax("GET", entry.rate, { target: "#console-rating", swap: "innerHTML" });
  };

  const markRounds = () => {
    if (!state) return;
    const now = serverNow();
    for (const li of rounds) {
      const round = Number(li.dataset.round);
      const entry = state.mine.find((e) => e.round === round);
      li.classList.toggle("on", !!entry && entry.starts_at <= now && now < entry.ends_at);
      li.classList.toggle("done", !!entry && entry.ends_at <= now);
    }
  };

  const tick = () => {
    if (!state) return;
    const now = serverNow();
    if (clockLine) clockLine.textContent = headline(state, now);
    markRounds();
    const current = state.mine.find((e) => e.starts_at <= now && now < e.ends_at) ?? null;
    if (state.status === "cancelled") {
      showCard("This sprint was cancelled", "Your recruiter will be in touch.");
      return;
    }
    if (current) {
      showRoom(current);
      return;
    }
    shown = null;
    const ended = lastEnded(state, now);
    if (ended) askRating(ended);
    const next = nextMine(state, now);
    if (next) {
      const who = cfg.role === "candidate" ? "Your next conversation is with " + next.with : "Next up: " + next.with;
      showCard(who, cfg.role === "candidate" ? "The room opens here when it starts. Keep this tab open; check your camera and microphone." : "The room opens here when the round starts. Rate the last conversation while you wait.", formatCountdown(next.starts_at - now));
      return;
    }
    if (now < state.starts_at) {
      showCard("Waiting for the start", "Nothing is scheduled for you in the first rounds yet.", formatCountdown(state.starts_at - now));
      return;
    }
    if (cfg.role === "candidate") showCard("That was everyone", "Thank you. Your recruiter will be in touch about next steps. You can close this page.");
    else showCard("Your rounds are done", "Every conversation of yours has ended. File any rating you still owe, then read the summary.");
  };

  const poll = async () => {
    try {
      const res = await fetch(cfg.state_url, { credentials: "same-origin", headers: { Accept: "application/json" } });
      if (res.ok) {
        const next = (await res.json()) as SprintState;
        offset = clockOffset(next, Date.now());
        state = next;
        root.dataset.phase = next.phase;
        tick();
      }
    } catch {
      // keep the last state; the next poll will try again
    }
  };
  void poll();
  window.setInterval(() => void poll(), STATE_POLL_MS);
  window.setInterval(tick, 250);
}

function boot(): void {
  const roomRoot = document.getElementById("room");
  if (roomRoot) {
    const cfg = readConfig<RoomConfig>(roomRoot);
    if (cfg) mountRoom(roomRoot, cfg);
  }
  const consoleRoot = document.getElementById("console");
  if (consoleRoot) {
    const cfg = readConfig<ConsoleConfig>(consoleRoot);
    if (cfg) mountConsole(consoleRoot, cfg);
  }
}

if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", boot);
else boot();
