import { createFileRoute } from "@tanstack/react-router";

// Placeholder for the Verself out-of-box experience (OOBE). The IAM login
// canary GETs this page as the post-login landing step of its new-user
// flow; the real OOBE replaces this route when the Verself surface lands.
// Server-rendered plain HTML on purpose: no client bundle, no styling
// dependencies, stable for synthetic checks.
const html = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Verself</title>
<style>
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 16px/1.5 system-ui, sans-serif; background: #0b0c0e; color: #e8e6e1; }
  main { max-width: 28rem; padding: 2rem; }
  h1 { font-size: 1.25rem; font-weight: 600; letter-spacing: 0.01em; margin: 0 0 0.5rem; }
  p { margin: 0; color: #9a968e; }
</style>
</head>
<body>
<main data-verself-oobe="placeholder">
  <h1>Verself</h1>
  <p>Account setup is not open yet.</p>
</main>
</body>
</html>
`;

const headers = {
  "cache-control": "no-store",
  "content-type": "text/html; charset=utf-8",
} as const;

export const Route = createFileRoute("/verself")({
  server: {
    handlers: {
      HEAD: () => new Response(null, { status: 200, headers }),
      GET: () => new Response(html, { status: 200, headers }),
    },
  },
});
