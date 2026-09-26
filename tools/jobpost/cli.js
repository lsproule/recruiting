#!/usr/bin/env node
// jobpost: place one posting on a board through a real browser and report
// where it landed. Request in on stdin (JSON), result out on stdout (JSON).
import { chromium } from "playwright";
import { boards } from "./boards/index.js";

async function readStdin() {
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8");
}

function fail(message, code = 1) {
  process.stdout.write(JSON.stringify({ error: message }) + "\n");
  process.exit(code);
}

const [command = "post"] = process.argv.slice(2);
if (command !== "post") fail(`unknown command ${command}; usage: jobpost post < request.json`, 2);

const raw = await readStdin();
let req;
try {
  req = JSON.parse(raw);
} catch (err) {
  fail(`request is not JSON: ${err.message}`, 2);
}
for (const field of ["board", "title", "body", "apply_url"]) {
  if (typeof req[field] !== "string" || req[field].trim() === "") fail(`request needs a ${field}`, 2);
}
const board = boards[req.board];
if (!board) fail(`no adapter for board ${req.board}; have ${Object.keys(boards).join(", ")}`, 2);

const headful = process.env.JOBPOST_HEADFUL === "1";
const browser = await chromium.launch({ headless: !headful });
try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(Number(process.env.JOBPOST_TIMEOUT_MS ?? 45_000));
  const result = await board.post(page, req, {
    env: process.env,
    dryRun: process.env.JOBPOST_DRY_RUN === "1",
    log: (line) => process.stderr.write(`[jobpost:${req.board}] ${line}\n`),
  });
  process.stdout.write(JSON.stringify(result) + "\n");
} catch (err) {
  fail(err instanceof Error ? err.message : String(err));
} finally {
  await browser.close();
}
