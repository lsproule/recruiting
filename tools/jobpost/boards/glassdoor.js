// Glassdoor employer postings are Indeed postings: since 2019 a job posted
// through Indeed's employer account appears on Glassdoor too. The adapter
// posts through Indeed and says so, rather than pretending Glassdoor has a
// flow of its own.
import { postOnIndeed } from "./indeed.js";

export async function post(page, posting, ctx) {
  ctx.log("Glassdoor postings go through the Indeed employer account and syndicate to Glassdoor");
  return postOnIndeed(page, posting, ctx, "glassdoor");
}
