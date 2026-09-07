// These run the real keymaps against a real EditorView, so the keys the
// assessment promises — vim's dd, emacs' C-k — are checked rather than
// assumed. The DOM comes from jsdom and is installed before the CodeMirror
// modules load, which is why they are imported inside the tests.

import test from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";

function installDOM(): void {
  const dom = new JSDOM("<!doctype html><html><body></body></html>", { pretendToBeVisual: true });
  const g = globalThis as unknown as Record<string, unknown>;
  const w = dom.window as unknown as Record<string, unknown>;
  g.window = w;
  g.Window = w.Window;
  g.document = w.document;
  for (const name of [
    "navigator",
    "getComputedStyle",
    "requestAnimationFrame",
    "cancelAnimationFrame",
    "MutationObserver",
    "ResizeObserver",
    "IntersectionObserver",
    "KeyboardEvent",
    "MouseEvent",
    "Event",
    "HTMLElement",
    "Element",
    "Node",
    "Range",
    "DOMParser",
    "DOMRect",
    "Selection",
  ]) {
    if (w[name] !== undefined && (g[name] === undefined || name === "navigator")) {
      Object.defineProperty(g, name, { value: w[name], configurable: true, writable: true });
    }
  }
}

installDOM();

async function editor(keymapName: "vim" | "emacs", doc: string) {
  const { EditorState, Compartment } = await import("@codemirror/state");
  const { EditorView, keymap } = await import("@codemirror/view");
  const { defaultKeymap, history, historyKeymap } = await import("@codemirror/commands");
  const { keymapExtension } = await import("./keymap");
  const conf = new Compartment();
  const host = document.createElement("div");
  document.body.appendChild(host);
  const view = new EditorView({
    state: EditorState.create({
      doc,
      extensions: [conf.of(keymapExtension(keymapName)), history(), keymap.of([...defaultKeymap, ...historyKeymap])],
    }),
    parent: host,
  });
  return { view, conf };
}

function press(view: { contentDOM: HTMLElement }, key: string, mods: { ctrlKey?: boolean; shiftKey?: boolean } = {}): void {
  // The emacs handler reads event.code, the CodeMirror keymap reads
  // event.key; a browser sends both, so the synthetic event does too.
  const code = key.length === 1 ? "Key" + key.toUpperCase() : key;
  const ev = new (globalThis as unknown as { KeyboardEvent: typeof KeyboardEvent }).KeyboardEvent("keydown", {
    key,
    code,
    bubbles: true,
    cancelable: true,
    ...mods,
  });
  view.contentDOM.dispatchEvent(ev);
}

test("vim dd deletes the line and the change reaches the document", async () => {
  const { view } = await editor("vim", "alpha\nbeta\ngamma\n");
  const { getCM, Vim } = await import("@replit/codemirror-vim");
  const cm = getCM(view);
  assert.ok(cm, "vim mode did not attach to the view");
  Vim.handleKey(cm, "d", "user");
  Vim.handleKey(cm, "d", "user");
  assert.equal(view.state.doc.toString(), "beta\ngamma\n");
  view.destroy();
});

test("an ex quit leaves the session open", async () => {
  const { view } = await editor("vim", "alpha\n");
  const { getCM, Vim } = await import("@replit/codemirror-vim");
  const cm = getCM(view);
  for (const cmd of ["q", "wq", "x"]) {
    Vim.handleEx(cm, cmd);
  }
  assert.equal(view.state.doc.toString(), "alpha\n", "an ex command changed the document");
  assert.ok(view.dom.isConnected, "an ex command tore the editor down");
  view.destroy();
});

test("emacs C-k kills to the end of the line", async () => {
  const { view } = await editor("emacs", "alpha beta\ngamma\n");
  view.dispatch({ selection: { anchor: 5 } });
  press(view, "k", { ctrlKey: true });
  assert.equal(view.state.doc.toString(), "alpha\ngamma\n");
  view.destroy();
});

test("Mod-Enter reaches a run binding through the vim keymap", async () => {
  const { EditorState, Compartment, Prec } = await import("@codemirror/state");
  const { EditorView, keymap } = await import("@codemirror/view");
  const { defaultKeymap } = await import("@codemirror/commands");
  const { keymapExtension } = await import("./keymap");
  let ran = 0;
  const conf = new Compartment();
  const host = document.createElement("div");
  document.body.appendChild(host);
  const view = new EditorView({
    state: EditorState.create({
      doc: "x\n",
      extensions: [
        Prec.highest(keymap.of([{ key: "Mod-Enter", preventDefault: true, run: () => ((ran += 1), true) }])),
        conf.of(keymapExtension("emacs")),
        keymap.of(defaultKeymap),
      ],
    }),
    parent: host,
  });
  press(view, "Enter", { ctrlKey: true });
  assert.equal(ran, 1, "the emacs keymap shadowed Mod-Enter");
  view.dispatch({ effects: conf.reconfigure(keymapExtension("vim")) });
  press(view, "Enter", { ctrlKey: true });
  assert.equal(ran, 2, "the vim keymap shadowed Mod-Enter");
  view.destroy();
});
