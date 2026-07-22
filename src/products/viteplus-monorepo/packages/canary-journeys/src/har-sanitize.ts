import type { RedactionRegistry } from "./redact.ts";

// HAR files are the canonical credential-leak vector (session cookies,
// Authorization headers, OAuth codes in URLs — the 2023 Okta support breach
// shape). Nothing exports a HAR unsanitized: strip the known carrier fields
// structurally, then run every remaining string through the redaction
// registry.

const SENSITIVE_HEADERS = new Set([
  "authorization",
  "cookie",
  "set-cookie",
  "proxy-authorization",
  "x-github-token",
]);

const SENSITIVE_QUERY_PARAMS = new Set([
  "access_token",
  "client_secret",
  "code",
  "id_token",
  "refresh_token",
  "session_state",
  "state",
  "token",
]);

const STRIPPED = "[STRIPPED]";

interface HarNameValue {
  name: string;
  value: string;
  [key: string]: unknown;
}

function stripNameValues(items: unknown, sensitive: Set<string>): void {
  if (!Array.isArray(items)) {
    return;
  }
  for (const item of items as HarNameValue[]) {
    if (typeof item?.name === "string" && sensitive.has(item.name.toLowerCase())) {
      item.value = STRIPPED;
    }
  }
}

function stripUrl(url: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url;
  }
  let changed = false;
  for (const key of [...parsed.searchParams.keys()]) {
    if (SENSITIVE_QUERY_PARAMS.has(key.toLowerCase())) {
      parsed.searchParams.set(key, STRIPPED);
      changed = true;
    }
  }
  return changed ? parsed.toString() : url;
}

function scrubStrings(value: unknown, registry: RedactionRegistry): unknown {
  if (typeof value === "string") {
    return registry.scrub(value);
  }
  if (Array.isArray(value)) {
    return value.map((entry) => scrubStrings(entry, registry));
  }
  if (value !== null && typeof value === "object") {
    const record = value as Record<string, unknown>;
    for (const key of Object.keys(record)) {
      record[key] = scrubStrings(record[key], registry);
    }
    return record;
  }
  return value;
}

export function sanitizeHar(har: unknown, registry: RedactionRegistry): unknown {
  const log = (har as { log?: { entries?: unknown[] } })?.log;
  for (const entry of log?.entries ?? []) {
    const e = entry as {
      request?: {
        url?: string;
        headers?: unknown;
        cookies?: unknown[];
        queryString?: unknown;
        postData?: { text?: string };
      };
      response?: {
        headers?: unknown;
        cookies?: unknown[];
        content?: { text?: string };
        redirectURL?: string;
      };
    };
    if (e.request) {
      if (typeof e.request.url === "string") {
        e.request.url = stripUrl(e.request.url);
      }
      stripNameValues(e.request.headers, SENSITIVE_HEADERS);
      stripNameValues(e.request.queryString, SENSITIVE_QUERY_PARAMS);
      e.request.cookies = [];
      if (e.request.postData?.text !== undefined) {
        e.request.postData.text = STRIPPED;
      }
    }
    if (e.response) {
      stripNameValues(e.response.headers, SENSITIVE_HEADERS);
      e.response.cookies = [];
      if (typeof e.response.redirectURL === "string") {
        e.response.redirectURL = stripUrl(e.response.redirectURL);
      }
      if (e.response.content?.text !== undefined) {
        e.response.content.text = STRIPPED;
      }
    }
  }
  return scrubStrings(har, registry);
}
