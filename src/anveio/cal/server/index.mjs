// anveio.com's origin: serves the `vp build` output in ../dist with no
// dependencies beyond node: builtins. Every file is read into memory at
// startup and requests are answered from that table only, so no request path
// ever reaches the filesystem and traversal has nothing to escape into.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, relative, sep } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../dist/", import.meta.url));
const port = Number(process.env.PORT ?? 8080);
const host = process.env.HOST ?? "0.0.0.0";
// www.<canonical> answers with a permanent redirect to the apex.
const canonicalHost = process.env.ANVEIO_CANONICAL_HOST ?? "anveio.com";

const types = {
  ".avif": "image/avif",
  ".css": "text/css; charset=utf-8",
  ".gif": "image/gif",
  ".html": "text/html; charset=utf-8",
  ".ico": "image/x-icon",
  ".jpeg": "image/jpeg",
  ".jpg": "image/jpeg",
  ".js": "text/javascript; charset=utf-8",
  ".json": "application/json; charset=utf-8",
  ".map": "application/json; charset=utf-8",
  ".mjs": "text/javascript; charset=utf-8",
  ".mp3": "audio/mpeg",
  ".mp4": "video/mp4",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".txt": "text/plain; charset=utf-8",
  ".wasm": "application/wasm",
  ".webm": "video/webm",
  ".webmanifest": "application/manifest+json",
  ".webp": "image/webp",
  ".wgsl": "text/wgsl; charset=utf-8",
  ".woff": "font/woff",
  ".woff2": "font/woff2",
  ".xml": "application/xml; charset=utf-8",
};

// Vite content-hashes everything under /assets/, so a name never changes
// meaning. HTML must revalidate so a deploy is picked up on the next load.
function cacheControl(path) {
  if (path.startsWith("/assets/")) return "public, max-age=31536000, immutable";
  if (path.endsWith(".html")) return "no-cache";
  return "public, max-age=3600";
}

const files = new Map();
for (const entry of readdirSync(root, { recursive: true, withFileTypes: true })) {
  if (!entry.isFile()) continue;
  const abs = join(entry.parentPath, entry.name);
  const path = "/" + relative(root, abs).split(sep).join("/");
  const body = readFileSync(abs);
  files.set(path, {
    body,
    etag: `"${createHash("sha256").update(body).digest("base64url").slice(0, 27)}"`,
    type: types[extname(path).toLowerCase()] ?? "application/octet-stream",
    cache: cacheControl(path),
  });
}
const index = files.get("/index.html");
if (!index) throw new Error(`no index.html under ${root}`);

const security = {
  "x-content-type-options": "nosniff",
  "referrer-policy": "strict-origin-when-cross-origin",
  "content-security-policy": "frame-ancestors 'none'",
};
// The sign-in popup's opener keeps its handle on the popup (closed polling);
// same-origin would sever it. The popup's own landing page sends no COOP so
// it never forces a browsing-context-group switch of its own.
const coop = { "cross-origin-opener-policy": "same-origin-allow-popups" };

function send(req, res, status, headers, body) {
  const h = { ...security, ...headers };
  if (body !== undefined) h["content-length"] = String(body.length);
  res.writeHead(status, h);
  res.end(req.method === "HEAD" ? undefined : body);
}

function text(req, res, status, message, headers = {}) {
  send(
    req,
    res,
    status,
    { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store", ...headers },
    Buffer.from(message + "\n"),
  );
}

function redirect(req, res, location) {
  send(req, res, 301, { location, "cache-control": "public, max-age=3600" }, Buffer.alloc(0));
}

function resolve(path) {
  if (path === "/") return index;
  if (path === "/privacy") return files.get("/privacy.html");
  const file = files.get(path);
  if (file) return file;
  // Client-side routes have no extension; anything with one is a real miss.
  const last = path.slice(path.lastIndexOf("/") + 1);
  return last.includes(".") ? undefined : index;
}

let draining = false;

const server = createServer((req, res) => {
  let url;
  try {
    url = new URL(req.url ?? "/", "http://origin");
  } catch {
    return text(req, res, 400, "Bad request");
  }
  const path = url.pathname;

  if (path === "/livez") return text(req, res, 200, "ok");
  if (path === "/healthz")
    return text(req, res, draining ? 503 : 200, draining ? "draining" : "ok");

  if (req.method !== "GET" && req.method !== "HEAD") {
    return text(req, res, 405, "Method not allowed", { allow: "GET, HEAD" });
  }

  const reqHost = (req.headers.host ?? "").replace(/:\d+$/, "").toLowerCase();
  if (reqHost === `www.${canonicalHost}`) {
    return redirect(req, res, `https://${canonicalHost}${path}${url.search}`);
  }
  // The prototype lived at /cal before it became the whole site.
  if (path === "/cal" || path.startsWith("/cal/")) return redirect(req, res, `/${url.search}`);

  const file = resolve(path);
  if (!file) return text(req, res, 404, "Not found");

  const headers = {
    "content-type": file.type,
    "cache-control": file.cache,
    etag: file.etag,
    ...(file.type.startsWith("text/html") && !path.startsWith("/oauth/") ? coop : {}),
  };
  if (req.headers["if-none-match"] === file.etag) return send(req, res, 304, headers);
  return send(req, res, 200, headers, file.body);
});

server.listen(port, host, () => {
  process.stdout.write(`anveio-cal serving ${files.size} files on ${host}:${port}\n`);
});

// Node as PID 1 gets no default signal handling. Fail readiness and keep
// serving while the endpoint is withdrawn from the ingress, then finish
// in-flight requests and exit.
process.on("SIGTERM", () => {
  draining = true;
  setTimeout(() => {
    server.close(() => process.exit(0));
    server.closeIdleConnections();
    setTimeout(() => process.exit(0), 10_000).unref();
  }, 5_000);
});
