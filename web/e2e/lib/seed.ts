import fs from "node:fs";
import path from "node:path";

import { repoRoot } from "./paths";

interface SeedProblem {
  title: string;
  kind: string;
  difficulty?: string;
  reference_solutions: { language: string; source: string }[];
}

/**
 * seedProblem reads one problem out of the committed seed bank. The suite
 * types the bank's own reference solution into the editor, so a passing run
 * stays correct when the bank changes.
 */
export function seedProblem(title: string): SeedProblem {
  const dir = path.join(repoRoot, "seed", "problems");
  for (const name of fs.readdirSync(dir).filter((n) => n.endsWith(".json"))) {
    const problems = JSON.parse(fs.readFileSync(path.join(dir, name), "utf8")) as SeedProblem[];
    const found = problems.find((p) => p.title === title);
    if (found) return found;
  }
  throw new Error(`seed bank has no problem titled ${title}`);
}

/** referenceSolution is the seed's own solution for one language. */
export function referenceSolution(title: string, language: string): string {
  const ref = seedProblem(title).reference_solutions.find((r) => r.language === language);
  if (!ref) throw new Error(`${title} has no ${language} reference solution`);
  return ref.source;
}
