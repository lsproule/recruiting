// Editor keymaps. The choice lives in a Compartment so switching
// reconfigures the running editor: the document, the selection, and the undo
// history all survive the swap.

import { Compartment, type Extension, type StateEffect } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { vim, Vim } from "@replit/codemirror-vim";
import { emacs } from "@replit/codemirror-emacs";

export type KeymapName = "default" | "vim" | "emacs";

export const KEYMAPS: KeymapName[] = ["default", "vim", "emacs"];

export const KEYMAP_LABELS: Record<KeymapName, string> = {
  default: "Default",
  vim: "Vim",
  emacs: "Emacs",
};

// STORAGE_KEY is shared by every editor on the page and by the try-it page,
// so a candidate configures the keymap once.
export const STORAGE_KEY = "recruiting.keymap";

export function isKeymapName(v: unknown): v is KeymapName {
  return typeof v === "string" && (KEYMAPS as string[]).includes(v);
}

// An ex command must never end the session: the only way out of an
// assessment is the Finish button, which submits. Writing is the periodic
// source sync, so :w has nothing to do either.
const EX_NO_OPS: Array<[string, string]> = [
  ["q", "q"],
  ["quit", "quit"],
  ["qa", "qa"],
  ["quitall", "quitall"],
  ["w", "w"],
  ["write", "write"],
  ["wq", "wq"],
  ["x", "x"],
  ["xit", "xit"],
  ["wqa", "wqa"],
];
for (const [name, prefix] of EX_NO_OPS) {
  Vim.defineEx(name, prefix, () => {});
}

// keymapExtension is what the compartment holds. vim's status panel is the
// mode indicator; the default keymap needs nothing beyond what the editor
// already installs.
export function keymapExtension(name: KeymapName): Extension {
  switch (name) {
    case "vim":
      return vim({ status: true });
    case "emacs":
      return emacs();
    default:
      return [];
  }
}

// readKeymap resolves the stored choice, falling back to default for a
// missing, unreadable, or unknown value.
export function readKeymap(storage: Storage | null): KeymapName {
  try {
    const v = storage?.getItem(STORAGE_KEY);
    return isKeymapName(v) ? v : "default";
  } catch {
    return "default";
  }
}

export function writeKeymap(storage: Storage | null, name: KeymapName): void {
  try {
    storage?.setItem(STORAGE_KEY, name);
  } catch {
    // Storage is a convenience; the editor still switches.
  }
}

// keymapEffect is the reconfiguration to dispatch. Dispatching an effect
// rather than rebuilding the state is what keeps the document and history.
export function keymapEffect(conf: Compartment, name: KeymapName): StateEffect<unknown> {
  return conf.reconfigure(keymapExtension(name));
}

export function applyKeymap(view: EditorView, conf: Compartment, name: KeymapName): void {
  view.dispatch({ effects: keymapEffect(conf, name) });
}
