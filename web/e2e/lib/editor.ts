import { Locator, Page, expect } from "@playwright/test";

/** The pane of the problem currently on screen. */
export function currentPane(page: Page): Locator {
  return page.locator("#assess .assess-problem:not([hidden])");
}

/** The CodeMirror editing surface of the visible problem. */
export function editor(page: Page): Locator {
  return currentPane(page).locator(".assess-editor .cm-content");
}

/** The rendered text of the editor, newlines included. */
export async function editorText(page: Page): Promise<string> {
  return editor(page).evaluate((el) =>
    Array.from(el.querySelectorAll(".cm-line"))
      .map((line) => (line.textContent === "​" ? "" : line.textContent))
      .join("\n"),
  );
}

/**
 * replaceSource clears the editor and inserts source as one input event, so
 * CodeMirror takes it whole rather than re-indenting it line by line.
 */
export async function replaceSource(page: Page, source: string): Promise<void> {
  const cm = editor(page);
  await cm.click();
  await page.keyboard.press("ControlOrMeta+a");
  await page.keyboard.press("Backspace");
  await page.keyboard.insertText(source);
  await expect(cm).toContainText(source.trim().split("\n")[0]);
}

/**
 * chooseLanguage picks a language for the visible problem. The toolbar also
 * holds the keymap control, so this addresses the bar's own select rather than
 * any select inside it.
 */
export async function chooseLanguage(page: Page, language: string): Promise<void> {
  await currentPane(page).locator(".assess-bar > select").selectOption(language);
}

/** chooseKeymap switches the editor keymap for every problem on the page. */
export async function chooseKeymap(page: Page, keymap: "default" | "vim" | "emacs"): Promise<void> {
  await page.locator("#assess .assess-keymap select").selectOption(keymap);
}

/** results is the output area of the visible problem. */
export function results(page: Page): Locator {
  return currentPane(page).locator(".assess-results");
}
