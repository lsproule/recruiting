import { chooseLanguage, replaceSource, results } from "../lib/editor";
import { recruiterStatePath } from "../lib/paths";
import { referenceSolution } from "../lib/seed";
import { expect, test } from "../lib/test";

test.use({ storageState: recruiterStatePath });

// Compiling rust in the sandbox is the slowest thing the suite does.
test.describe.configure({ mode: "serial", timeout: 300_000 });

for (const language of ["python", "rust"]) {
  test(`Try it runs ${language} against the real runner`, async ({ page, fixture }) => {
    await page.goto(`/app/problems/${fixture.problemId}/try`);
    await expect(page.locator(".try-head")).toContainText("Try it");
    await expect(page.locator("#assess .assess-editor .cm-content")).toBeVisible();

    await chooseLanguage(page, language);
    await replaceSource(page, referenceSolution(fixture.problemTitle, language));
    await page.getByRole("button", { name: "Run example cases" }).click();

    const out = results(page);
    await expect(out.locator("p").first()).toHaveText(/^(\d+) of \1 example cases passed$/, {
      timeout: 240_000,
    });
    const cases = out.locator(".assess-case");
    expect(await cases.count()).toBeGreaterThan(0);
    for (const text of await cases.allTextContents()) {
      expect(text).toContain(": pass ");
    }
  });
}
