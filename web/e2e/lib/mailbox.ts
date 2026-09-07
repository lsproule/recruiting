import { APIRequestContext, request } from "@playwright/test";

const mailpitURL = process.env.E2E_MAILPIT_URL ?? "http://localhost:8025";

/**
 * waitForLink polls Mailpit for the newest message to `to` whose subject
 * contains `subject`, and returns the first link in its text part matching
 * `match`. Mail travels through the worker, so it arrives a moment after
 * whatever queued it.
 */
export async function waitForLink(
  to: string,
  subject: string,
  match: RegExp,
  timeoutMs = 60_000,
): Promise<string> {
  const api: APIRequestContext = await request.newContext({ baseURL: mailpitURL });
  try {
    const deadline = Date.now() + timeoutMs;
    let lastSeen = "";
    while (Date.now() < deadline) {
      const res = await api.get("/api/v1/search", {
        params: { query: `to:${to}` },
      });
      if (res.ok()) {
        const body = (await res.json()) as {
          messages: { ID: string; Subject: string }[];
        };
        const hit = body.messages.find((m) => m.Subject.includes(subject));
        if (hit) {
          const msg = (await (await api.get(`/api/v1/message/${hit.ID}`)).json()) as {
            Text?: string;
            HTML?: string;
          };
          const text = `${msg.Text ?? ""}\n${msg.HTML ?? ""}`;
          const link = text.match(match);
          if (link) return link[0];
          lastSeen = `message ${hit.ID} carried no link matching ${match}`;
        }
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
    throw new Error(`no "${subject}" mail for ${to} within ${timeoutMs}ms${lastSeen ? `: ${lastSeen}` : ""}`);
  } finally {
    await api.dispose();
  }
}
