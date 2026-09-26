import fs from "node:fs";
import path from "node:path";

import { repoRoot } from "./paths";

interface SeedProblem {
  title: string;
  kind: string;
  difficulty?: string;
  reference_solutions: { language: string; source: string }[];
}

/** The solution file names the bank uses, by language; mirrors seed/problems/embed.go. */
const SOLUTION_FILES: Record<string, string> = {
  python: "solution.py",
  javascript: "solution.js",
  ruby: "solution.rb",
  php: "solution.php",
  go: "solution.go",
  java: "Solution.java",
  csharp: "Solution.cs",
  cpp: "solution.cpp",
  c: "solution.c",
  rust: "solution.rs",
  sql: "solution.sql",
};

/**
 * seedProblem reads one problem out of the committed seed bank: its
 * problem.json plus the solution files beside it. The suite types the bank's
 * own reference solution into the editor, so a passing run stays correct when
 * the bank changes.
 */
export function seedProblem(title: string): SeedProblem {
  const bank = path.join(repoRoot, "seed", "problems", "_bank");
  for (const dir of fs.readdirSync(bank)) {
    const file = path.join(bank, dir, "problem.json");
    if (!fs.existsSync(file)) continue;
    const problem = JSON.parse(fs.readFileSync(file, "utf8")) as SeedProblem;
    if (problem.title !== title) continue;
    problem.reference_solutions = [];
    for (const [language, name] of Object.entries(SOLUTION_FILES)) {
      const source = path.join(bank, dir, name);
      if (fs.existsSync(source)) problem.reference_solutions.push({ language, source: fs.readFileSync(source, "utf8") });
    }
    return problem;
  }
  throw new Error(`seed bank has no problem titled ${title}`);
}

/** referenceSolution is the seed's own solution for one language. */
export function referenceSolution(title: string, language: string): string {
  const ref = seedProblem(title).reference_solutions.find((r) => r.language === language);
  if (!ref) throw new Error(`${title} has no ${language} reference solution`);
  return ref.source;
}
