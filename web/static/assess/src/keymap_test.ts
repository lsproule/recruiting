import test from "node:test";
import assert from "node:assert/strict";
import { Compartment, EditorState } from "@codemirror/state";
import { history, undo, undoDepth } from "@codemirror/commands";
import { KEYMAPS, STORAGE_KEY, isKeymapName, keymapEffect, keymapExtension, readKeymap, writeKeymap } from "./keymap";

class FakeStorage implements Storage {
  private items = new Map<string, string>();
  get length(): number {
    return this.items.size;
  }
  clear(): void {
    this.items.clear();
  }
  getItem(k: string): string | null {
    return this.items.has(k) ? (this.items.get(k) as string) : null;
  }
  key(i: number): string | null {
    return Array.from(this.items.keys())[i] ?? null;
  }
  removeItem(k: string): void {
    this.items.delete(k);
  }
  setItem(k: string, v: string): void {
    this.items.set(k, v);
  }
  [name: string]: unknown;
}

function stateWith(conf: Compartment, doc: string) {
  return EditorState.create({ doc, extensions: [history(), conf.of(keymapExtension("default"))] });
}

test("switching the keymap keeps the document and the undo history", () => {
  const conf = new Compartment();
  let state = stateWith(conf, "line one\n");
  state = state.update({ changes: { from: state.doc.length, insert: "line two\n" } }).state;
  assert.equal(state.doc.toString(), "line one\nline two\n");
  assert.ok(undoDepth(state) > 0, "the edit should be undoable before the swap");

  for (const name of ["vim", "emacs", "default"] as const) {
    state = state.update({ effects: keymapEffect(conf, name) }).state;
    assert.equal(state.doc.toString(), "line one\nline two\n", name + " reset the document");
    assert.ok(undoDepth(state) > 0, name + " reset the history");
  }

  // The history is not merely present, it still runs.
  let applied: EditorState | null = null;
  undo({
    state,
    dispatch: (tr) => {
      applied = tr.state;
    },
  });
  assert.ok(applied, "undo did not produce a transaction after the swap");
  assert.equal((applied as unknown as EditorState).doc.toString(), "line one\n");
});

test("the keymap choice round-trips through storage", () => {
  const storage = new FakeStorage();
  assert.equal(readKeymap(storage), "default");
  for (const name of KEYMAPS) {
    writeKeymap(storage, name);
    assert.equal(storage.getItem(STORAGE_KEY), name);
    assert.equal(readKeymap(storage), name);
  }
  storage.setItem(STORAGE_KEY, "nano");
  assert.equal(readKeymap(storage), "default", "an unknown stored value falls back");
  assert.equal(readKeymap(null), "default");
});

test("only the three offered names are keymaps", () => {
  assert.deepEqual(KEYMAPS, ["default", "vim", "emacs"]);
  for (const name of KEYMAPS) assert.ok(isKeymapName(name));
  for (const name of ["nano", "", "Vim", null, 3]) assert.equal(isKeymapName(name), false);
});
