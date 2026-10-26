import * as v from "valibot";
import { STREAMS } from "./streams";

// The phone side of an upload: one multipart upload per video, three parts in
// flight, each part PUT straight to R2 on a freshly signed URL. Every failure
// short of a dead link is retried until it succeeds — rural mobile data drops
// out for minutes at a time — and the multipart session is remembered per
// file, so picking the same video again after a reload continues from the
// parts R2 already holds.

export type Phase = "waiting" | "uploading" | "offline" | "finishing" | "done" | "failed";

export interface Progress {
  readonly phase: Phase;
  readonly sent: number;
  readonly total: number;
  readonly seconds?: number | null;
}

const ReceiptSchema = v.object({
  filename: v.string(),
  size: v.number(),
  seconds: v.nullable(v.number()),
  name: v.string(),
  stream: v.picklist(STREAMS),
  at: v.string(),
});
export type Receipt = v.InferOutput<typeof ReceiptSchema>;
export const Receipts = v.array(ReceiptSchema);

const SessionSchema = v.object({ key: v.string(), uploadId: v.string(), partSize: v.number() });
type Session = v.InferOutput<typeof SessionSchema>;
const Sessions = v.record(v.string(), SessionSchema);
const Parts = v.object({
  parts: v.array(v.object({ n: v.number(), etag: v.string(), size: v.number() })),
});

// The link was revoked or mistyped: nothing to retry.
export class LinkError extends Error {}
// The server refused the request itself (a file over the size cap, say).
export class Refused extends Error {}
// R2 would not assemble the parts; the video starts over.
class Unassembled extends Error {}

const CONCURRENCY = 3;
const STALL_MS = 60_000;
const SESSIONS = "anveio.upload.sessions";
export const RECEIPTS = "anveio.upload.receipts";

class HttpError extends Error {
  constructor(readonly status: number) {
    super(`HTTP ${status}`);
  }
}

export function api(token: string) {
  return async <S extends v.GenericSchema>(
    op: string,
    body: object,
    schema: S,
  ): Promise<v.InferOutput<S>> => {
    const response = await fetch(`/upload/api/${op}`, {
      method: "POST",
      headers: { authorization: `Bearer ${token}`, "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    if (response.status === 401) throw new LinkError();
    if ([400, 404, 413, 422].includes(response.status)) {
      throw new Refused(`HTTP ${response.status}`);
    }
    if (response.status === 409) throw new Unassembled();
    if (!response.ok) throw new HttpError(response.status);
    return v.parse(schema, await response.json());
  };
}
type Api = ReturnType<typeof api>;

export async function upload(
  file: File,
  who: { name: string; stream: Receipt["stream"] },
  call: Api,
  report: (p: Progress) => void,
  fresh = false,
): Promise<Receipt> {
  const total = file.size;
  const done = new Map<number, { etag: string; size: number }>();
  const inFlight = new Map<number, number>();
  let phase: Phase = "uploading";
  const emit = () => {
    let sent = 0;
    for (const p of done.values()) sent += p.size;
    for (const n of inFlight.values()) sent += n;
    report({ phase, sent: Math.min(sent, total), total });
  };
  const retry = <T>(task: () => Promise<T>) =>
    retrying(task, (offline) => {
      phase = offline ? "offline" : "uploading";
      emit();
    });

  const id = await fingerprint(file);
  const meta = {
    ...who,
    filename: file.name,
    size: file.size,
    type: file.type,
    lastModified: file.lastModified,
  };

  if (fresh) forget(id);
  let session = sessions()[id];
  if (session) {
    const known = session;
    const status = await retry(() =>
      call("status", known, Parts).catch((e) => {
        if (e instanceof HttpError && e.status === 410) return undefined;
        throw e;
      }),
    );
    if (status) for (const p of status.parts) done.set(p.n, p);
    else session = undefined;
  }
  if (!session) {
    session = await retry(() => call("start", meta, SessionSchema));
    remember(id, session);
  }
  const { key, uploadId, partSize } = session;
  const count = Math.max(1, Math.ceil(total / partSize));
  // A resumed part only counts if it is the size this file says it should be.
  for (const [n, p] of done) {
    if (p.size !== Math.min(partSize, total - (n - 1) * partSize)) done.delete(n);
  }
  emit();

  const queue = Array.from({ length: count }, (_, i) => i + 1).filter((n) => !done.has(n));
  const worker = async () => {
    for (let n = queue.shift(); n !== undefined; n = queue.shift()) {
      const part = n;
      const blob = file.slice((part - 1) * partSize, Math.min(part * partSize, total));
      const etag = await retry(async () => {
        const { url } = await call(
          "part",
          { key, uploadId, n: part },
          v.object({ url: v.string() }),
        );
        return put(url, blob, (loaded) => {
          inFlight.set(part, loaded);
          emit();
        }).finally(() => inFlight.delete(part));
      });
      done.set(part, { etag, size: blob.size });
      phase = "uploading";
      emit();
    }
  };
  // Once, if R2 drops the upload or won't assemble it, the video starts over.
  const restart = (error: unknown) => {
    if (error instanceof Unassembled && !fresh) return undefined;
    throw error;
  };
  const sent = await Promise.all(
    Array.from({ length: Math.min(CONCURRENCY, queue.length) }, worker),
  ).then(
    () => true,
    (error: unknown) => {
      queue.length = 0;
      return restart(error);
    },
  );
  if (!sent) return upload(file, who, call, report, true);

  phase = "finishing";
  emit();
  const parts = [...done].map(([n, p]) => ({ n, etag: p.etag }));
  const completed = await retry(() =>
    call(
      "complete",
      { key, uploadId, parts, ...meta },
      v.object({ seconds: v.nullable(v.number()) }),
    ),
  ).catch(restart);
  if (!completed) return upload(file, who, call, report, true);
  const { seconds } = completed;
  forget(id);
  const receipt: Receipt = {
    filename: file.name,
    size: total,
    seconds,
    name: who.name,
    stream: who.stream,
    at: new Date().toISOString(),
  };
  write(RECEIPTS, [receipt, ...read(RECEIPTS, Receipts, [])].slice(0, 500));
  report({ phase: "done", sent: total, total, seconds });
  return receipt;
}

// Retries everything but a dead link or a refused request: waits out offline spells, then
// backs off 1 s, 2 s, 4 s … up to 30 s between attempts.
async function retrying<T>(
  task: () => Promise<T>,
  waiting: (offline: boolean) => void,
): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await task();
    } catch (error) {
      if (error instanceof LinkError || error instanceof Refused || error instanceof Unassembled)
        throw error;
      if (!navigator.onLine) {
        waiting(true);
        await new Promise((resolve) => window.addEventListener("online", resolve, { once: true }));
      }
      waiting(false);
      const delay = Math.min(30_000, 1000 * 2 ** attempt) * (0.5 + Math.random() / 2);
      await new Promise((resolve) => setTimeout(resolve, delay));
    }
  }
}

// XHR rather than fetch: only XHR reports upload progress. A part that stops
// moving for a minute is abandoned and retried.
function put(url: string, blob: Blob, progress: (loaded: number) => void): Promise<string> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let stall = setTimeout(() => xhr.abort(), STALL_MS);
    const settle = () => clearTimeout(stall);
    xhr.upload.onprogress = (e) => {
      clearTimeout(stall);
      stall = setTimeout(() => xhr.abort(), STALL_MS);
      progress(e.loaded);
    };
    xhr.onload = () => {
      settle();
      const etag = xhr.getResponseHeader("etag");
      if (xhr.status >= 200 && xhr.status < 300 && etag) resolve(etag);
      // R2 no longer knows the upload (aborted as stale): start the video over.
      else if (xhr.status === 404) reject(new Unassembled());
      else reject(new HttpError(xhr.status));
    };
    xhr.onerror = xhr.onabort = () => {
      settle();
      reject(new HttpError(0));
    };
    xhr.open("PUT", url);
    xhr.send(blob);
  });
}

// Which saved session a picked file resumes. Android's photo picker can stamp
// lastModified with the moment of picking, so the file is recognised by its
// name, size and a digest of its first and last 64 KiB instead.
async function fingerprint(file: File): Promise<string> {
  const edge = 64 * 1024;
  const sample = new Blob([file.slice(0, edge), file.slice(Math.max(edge, file.size - edge))]);
  const digest = await crypto.subtle.digest("SHA-256", await sample.arrayBuffer());
  const hex = Array.from(new Uint8Array(digest), (x) => x.toString(16).padStart(2, "0")).join("");
  return `${file.name}:${file.size}:${hex}`;
}

const sessions = () => read(SESSIONS, Sessions, {});
const remember = (id: string, s: Session) => write(SESSIONS, { ...sessions(), [id]: s });
const forget = (id: string) => {
  const { [id]: _, ...rest } = sessions();
  write(SESSIONS, rest);
};

export function read<S extends v.GenericSchema>(
  key: string,
  schema: S,
  fallback: v.InferOutput<S>,
): v.InferOutput<S> {
  try {
    const raw = localStorage.getItem(key);
    const parsed = raw === null ? undefined : v.safeParse(schema, JSON.parse(raw));
    return parsed?.success ? parsed.output : fallback;
  } catch {
    return fallback;
  }
}

export function write(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Private mode or a full quota: uploads still work, they just won't resume.
  }
}
