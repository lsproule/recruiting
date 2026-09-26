// Editor language modes, one per id in the platform's language registry.
// Ids without a dedicated CodeMirror 6 package fall back to the legacy
// stream modes, which highlight well enough for a timed screen.

import { StreamLanguage, type LanguageSupport } from "@codemirror/language";
import type { Extension } from "@codemirror/state";
import { python } from "@codemirror/lang-python";
import { javascript } from "@codemirror/lang-javascript";
import { sql } from "@codemirror/lang-sql";
import { go } from "@codemirror/lang-go";
import { java } from "@codemirror/lang-java";
import { cpp } from "@codemirror/lang-cpp";
import { rust } from "@codemirror/lang-rust";
import { php } from "@codemirror/lang-php";
import { ruby } from "@codemirror/legacy-modes/mode/ruby";
import { csharp } from "@codemirror/legacy-modes/mode/clike";

// Retired ids still reaching the island from a stored row or an old page.
const ALIASES: Record<string, string> = { node: "javascript" };

const MODES: Record<string, () => LanguageSupport | Extension> = {
  python: () => python(),
  javascript: () => javascript(),
  go: () => go(),
  java: () => java(),
  c: () => cpp(),
  cpp: () => cpp(),
  rust: () => rust(),
  php: () => php(),
  ruby: () => StreamLanguage.define(ruby),
  csharp: () => StreamLanguage.define(csharp),
  sql: () => sql(),
};

// Labels the language control shows; an id the registry grows past this map
// is shown as itself rather than hidden.
const LABELS: Record<string, string> = {
  python: "Python",
  javascript: "JavaScript",
  go: "Go",
  java: "Java",
  c: "C",
  cpp: "C++",
  rust: "Rust",
  php: "PHP",
  ruby: "Ruby",
  csharp: "C#",
  sql: "SQL",
};

export function normalizeLanguage(id: string): string {
  const lower = (id ?? "").trim().toLowerCase();
  return ALIASES[lower] ?? lower;
}

export function languageLabel(id: string): string {
  return LABELS[normalizeLanguage(id)] ?? id;
}

// hasLanguageMode reports whether an id highlights; the tests use it to hold
// the island to the whole registry.
export function hasLanguageMode(id: string): boolean {
  return normalizeLanguage(id) in MODES;
}

export function languageSupport(id: string): Extension {
  const mode = MODES[normalizeLanguage(id)];
  return mode ? mode() : [];
}
