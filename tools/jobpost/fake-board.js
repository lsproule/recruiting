#!/usr/bin/env node
// A tiny job board for the demo and the e2e suite: a posting form, a
// listing, and a page per posting whose apply button points back at the
// platform. Postings live in memory for the life of the process.
import http from "node:http";
import { URL } from "node:url";

const port = Number(process.argv[process.argv.indexOf("--port") + 1] || process.env.PORT || 8765);
const postings = [];

const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);

const shell = (title, body) => `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>${esc(title)} · Demo Job Board</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:48rem;margin:2rem auto;padding:0 1rem;color:#222}label{display:block;margin:1rem 0 .25rem}input,textarea{width:100%;padding:.5rem;font:inherit}button,.btn{background:#1a56db;color:#fff;border:0;padding:.6rem 1rem;border-radius:.4rem;font:inherit;text-decoration:none;display:inline-block}.card{border:1px solid #ddd;border-radius:.5rem;padding:1rem;margin:1rem 0}pre{white-space:pre-wrap}</style></head>
<body><header><a href="/jobs">Demo Job Board</a> · <a href="/post">Post a job</a></header>${body}</body></html>`;

function readBody(req) {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (c) => (data += c));
    req.on("end", () => resolve(new URLSearchParams(data)));
  });
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const send = (status, html) => {
    res.writeHead(status, { "content-type": "text/html; charset=utf-8" });
    res.end(html);
  };
  if (req.method === "GET" && (url.pathname === "/" || url.pathname === "/jobs")) {
    const list = postings.length
      ? postings.map((p) => `<div class="card"><h2><a href="/jobs/${p.id}">${esc(p.title)}</a></h2><p>${esc(p.body.split("\n")[0])}</p></div>`).join("")
      : "<p>No postings yet.</p>";
    return send(200, shell("Jobs", `<h1>Open roles</h1>${list}`));
  }
  if (req.method === "GET" && url.pathname === "/post") {
    return send(200, shell("Post a job", `<h1>Post a job</h1><form method="post" action="/post">
<label for="title">Title</label><input id="title" name="title" required>
<label for="body">Description</label><textarea id="body" name="body" rows="14" required></textarea>
<label for="apply_url">Apply URL</label><input id="apply_url" name="apply_url" type="url" required>
<p><button type="submit">Publish posting</button></p></form>`));
  }
  if (req.method === "POST" && url.pathname === "/post") {
    const form = await readBody(req);
    const p = { id: String(postings.length + 1), title: form.get("title") ?? "", body: form.get("body") ?? "", applyURL: form.get("apply_url") ?? "", at: new Date().toISOString() };
    postings.push(p);
    res.writeHead(303, { location: `/jobs/${p.id}` });
    return res.end();
  }
  const m = url.pathname.match(/^\/jobs\/(\d+)$/);
  if (req.method === "GET" && m) {
    const p = postings.find((x) => x.id === m[1]);
    if (!p) return send(404, shell("Not found", "<h1>No such posting</h1>"));
    return send(200, shell(p.title, `<h1>${esc(p.title)}</h1><p class="posted">Posted ${esc(p.at)}</p><pre>${esc(p.body)}</pre><p><a class="btn" href="${esc(p.applyURL)}">Apply</a></p>`));
  }
  if (req.method === "GET" && url.pathname === "/api/jobs") {
    res.writeHead(200, { "content-type": "application/json" });
    return res.end(JSON.stringify(postings));
  }
  send(404, shell("Not found", "<h1>Not found</h1>"));
});

server.listen(port, "127.0.0.1", () => {
  process.stderr.write(`demo job board listening on http://127.0.0.1:${port}\n`);
});
