// One adapter per board. Each exports post(page, posting, ctx) -> {url, id}
// and keeps every selector for its board to itself.
import * as demo from "./demo.js";
import * as indeed from "./indeed.js";
import * as linkedin from "./linkedin.js";
import * as glassdoor from "./glassdoor.js";

export const boards = { demo, indeed, linkedin, glassdoor };
