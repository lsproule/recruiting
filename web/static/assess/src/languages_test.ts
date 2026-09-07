import test from "node:test";
import assert from "node:assert/strict";
import { EditorState } from "@codemirror/state";
import { hasLanguageMode, languageLabel, languageSupport, normalizeLanguage } from "./languages";

// The platform's language registry, in its order. Every id has to highlight.
const REGISTRY = [
  "python",
  "javascript",
  "typescript",
  "go",
  "java",
  "c",
  "cpp",
  "rust",
  "php",
  "ruby",
  "haskell",
  "lua",
  "kotlin",
  "csharp",
  "sql",
];

test("every registry language has an editor mode", () => {
  for (const id of REGISTRY) {
    assert.ok(hasLanguageMode(id), id + " has no editor mode");
    const state = EditorState.create({ doc: "x", extensions: [languageSupport(id)] });
    assert.ok(state.doc.toString() === "x");
  }
});

test("the retired node id resolves to javascript", () => {
  assert.equal(normalizeLanguage(" Node "), "javascript");
  assert.ok(hasLanguageMode("node"));
  assert.equal(languageLabel("node"), "JavaScript");
});

test("an unknown language is inert rather than fatal", () => {
  assert.equal(hasLanguageMode("cobol"), false);
  assert.equal(languageLabel("cobol"), "cobol");
  const state = EditorState.create({ doc: "x", extensions: [languageSupport("cobol")] });
  assert.equal(state.doc.toString(), "x");
});
