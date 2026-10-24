import { createHash, randomUUID, timingSafeEqual } from "node:crypto";
import { readFile } from "node:fs/promises";
import * as v from "valibot";
import { mp4Seconds } from "./mp4";
import { STREAMS } from "./streams";
import { type Bucket, call, presign, S3Error, xmlAll, xmlText } from "./s3";

// anveio.com/upload's server half. Uploaders hold a shared link whose secret
// rides in the URL fragment and reaches us only as a bearer token; the
// registry of accepted links is a ConfigMap of SHA-256 digests, so revoking a
// leaked link is a one-line Git change that takes effect without a restart.
// Phones PUT 8+ MiB parts straight to R2 with URLs signed here; this side does
// the multipart bookkeeping and writes a manifest next to every finished video.

const MiB = 1024 * 1024;
const MAX_BYTES = 64 * 1024 * MiB;
const MAX_PARTS = 10_000;
const PART_URL_SECONDS = 3600;

const KEY = /^footage\/\d{4}-\d{2}-\d{2}\/[a-z]+\/[0-9a-f-]{36}(\.[a-z0-9]{1,5})?$/;

const Registry = v.object({
  links: v.array(
    v.object({ id: v.string(), sha256: v.pipe(v.string(), v.hexadecimal(), v.length(64)) }),
  ),
});

const Upload = v.object({
  key: v.pipe(v.string(), v.regex(KEY)),
  uploadId: v.pipe(v.string(), v.minLength(1), v.maxLength(1024)),
});

const Video = v.object({
  name: v.pipe(v.string(), v.trim(), v.minLength(1), v.maxLength(80)),
  stream: v.picklist(STREAMS),
  filename: v.pipe(v.string(), v.maxLength(255)),
  size: v.pipe(v.number(), v.integer(), v.minValue(1), v.maxValue(MAX_BYTES)),
  type: v.pipe(v.string(), v.maxLength(100)),
  lastModified: v.pipe(v.number(), v.integer(), v.minValue(0)),
});

const PartNumber = v.pipe(v.number(), v.integer(), v.minValue(1), v.maxValue(MAX_PARTS));

const Complete = v.object({
  ...Upload.entries,
  ...Video.entries,
  parts: v.pipe(
    v.array(v.object({ n: PartNumber, etag: v.pipe(v.string(), v.maxLength(128)) })),
    v.minLength(1),
    v.maxLength(MAX_PARTS),
  ),
});

const Receipt = v.object({ seconds: v.nullable(v.number()), size: v.number() });

type Link = v.InferOutput<typeof Registry>["links"][number];
type Context = { readonly b: Bucket; readonly link: Link; readonly request: Request };

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json", "cache-control": "no-store" },
  });

// Each operation validates its own body before it runs.
const op =
  <S extends v.GenericSchema>(
    schema: S,
    run: (ctx: Context, body: v.InferOutput<S>) => Promise<unknown>,
  ) =>
  async (ctx: Context, raw: unknown) => {
    const parsed = v.safeParse(schema, raw);
    return parsed.success
      ? json(200, await run(ctx, parsed.output))
      : json(400, { error: "invalid" });
  };

const OPS: Readonly<Record<string, (ctx: Context, raw: unknown) => Promise<Response>>> = {
  hello: op(v.object({}), async () => ({})),
  start: op(Video, ({ b }, video) => start(b, video)),
  part: op(v.object({ ...Upload.entries, n: PartNumber }), async ({ b }, { key, uploadId, n }) => ({
    url: presign(b, "PUT", key, { partNumber: String(n), uploadId }, PART_URL_SECONDS),
  })),
  status: op(Upload, ({ b }, upload) => status(b, upload)),
  complete: op(Complete, (ctx, upload) => complete(ctx, upload)),
};

function bucket(): Bucket | undefined {
  const env = process.env;
  const [endpoint, name, accessKeyId, secretAccessKey] = [
    env.FOOTAGE_S3_ENDPOINT,
    env.FOOTAGE_S3_BUCKET,
    env.FOOTAGE_S3_ACCESS_KEY_ID,
    env.FOOTAGE_S3_SECRET_ACCESS_KEY,
  ];
  if (!endpoint || !name || !accessKeyId || !secretAccessKey) return undefined;
  return { endpoint, bucket: name, accessKeyId, secretAccessKey };
}

async function authorize(request: Request): Promise<Link | undefined> {
  const token = /^Bearer ([A-Za-z0-9_-]{32,128})$/.exec(
    request.headers.get("authorization") ?? "",
  )?.[1];
  const file = process.env.FOOTAGE_LINKS_FILE;
  if (!token || !file) return undefined;
  const registry = v.parse(Registry, JSON.parse(await readFile(file, "utf8")));
  const digest = createHash("sha256").update(token).digest();
  return registry.links.find((link) => timingSafeEqual(Buffer.from(link.sha256, "hex"), digest));
}

// A request that can never succeed as sent; the phone gives that video up.
class Unacceptable extends Error {}

export async function handle(name: string, request: Request): Promise<Response> {
  const run = Object.hasOwn(OPS, name) ? OPS[name] : undefined;
  if (!run) return json(404, { error: "unknown" });
  const b = bucket();
  if (!b) return json(503, { error: "unconfigured" });
  const link = await authorize(request).catch((error: unknown) => {
    console.error("anveio-footage-links unreadable", error);
    return null;
  });
  if (link === null) return json(503, { error: "registry" });
  if (!link) return json(401, { error: "link" });
  try {
    return await run({ b, link, request }, await request.json().catch(() => undefined));
  } catch (error) {
    if (error instanceof S3Error && error.code === "NoSuchUpload")
      return json(410, { error: "gone" });
    // The parts don't assemble, or the upload vanished under them: the
    // phone starts that video over.
    if (
      error instanceof S3Error &&
      /^(InvalidPart|InvalidPartOrder|EntityTooSmall|NoSuchKey)$/.test(error.code)
    ) {
      return json(409, { error: "parts" });
    }
    if (error instanceof Unacceptable) return json(422, { error: error.message });
    throw error;
  }
}

async function start(b: Bucket, video: v.InferOutput<typeof Video>) {
  const ext = /\.([A-Za-z0-9]{1,5})$/.exec(video.filename)?.[1]?.toLowerCase();
  const day = new Date().toISOString().slice(0, 10);
  const key = `footage/${day}/${video.stream}/${randomUUID()}${ext ? `.${ext}` : ""}`;
  const headers: Record<string, string> = video.type ? { "content-type": video.type } : {};
  const xml = await (await call(b, "POST", key, { uploads: "" }, { headers })).text();
  const uploadId = xmlText(xml, "UploadId");
  if (!uploadId) throw new Error("CreateMultipartUpload answered without an UploadId");
  // R2 wants equal-sized parts (all but the last), at least 5 MiB each.
  const partSize = Math.max(8 * MiB, Math.ceil(video.size / MAX_PARTS / MiB) * MiB);
  return { key, uploadId, partSize };
}

async function status(b: Bucket, { key, uploadId }: v.InferOutput<typeof Upload>) {
  const parts: { n: number; etag: string; size: number }[] = [];
  let marker = "0";
  for (;;) {
    const query = { uploadId, "max-parts": "1000", "part-number-marker": marker };
    const xml = await (await call(b, "GET", key, query)).text();
    for (const part of xmlAll(xml, "Part")) {
      parts.push({
        n: Number(xmlText(part, "PartNumber")),
        etag: xmlText(part, "ETag") ?? "",
        size: Number(xmlText(part, "Size")),
      });
    }
    const next = xmlText(xml, "NextPartNumberMarker");
    if (xmlText(xml, "IsTruncated") !== "true" || !next) return { parts };
    marker = next;
  }
}

async function complete({ b, link, request }: Context, upload: v.InferOutput<typeof Complete>) {
  const parts = [...upload.parts].sort((x, y) => x.n - y.n);
  const xml = `<CompleteMultipartUpload>${parts
    .map((p) => `<Part><PartNumber>${p.n}</PartNumber><ETag>${escape(p.etag)}</ETag></Part>`)
    .join("")}</CompleteMultipartUpload>`;
  const manifestKey = upload.key
    .replace(/^footage\//, "manifests/")
    .replace(/(\.[a-z0-9]+)?$/, ".json");
  try {
    await call(b, "POST", upload.key, { uploadId: upload.uploadId }, { body: xml });
  } catch (error) {
    if (!(error instanceof S3Error && error.code === "NoSuchUpload")) throw error;
    // A retried complete whose first answer was lost: the object is already
    // whole, and its manifest, once written, is never rewritten.
    const written = await call(b, "GET", manifestKey).then(
      async (r) => v.safeParse(Receipt, await r.json()),
      () => undefined,
    );
    if (written?.success) return written.output;
  }
  const head = await call(b, "HEAD", upload.key).catch((error: unknown) => {
    throw error instanceof S3Error && error.status === 404 ? new S3Error(404, "NoSuchKey") : error;
  });
  const size = Number(head.headers.get("content-length"));
  if (size !== upload.size) throw new Unacceptable(`size ${size} != ${upload.size}`);
  const seconds = await mp4Seconds(async (start, length) => {
    const range = { range: `bytes=${start}-${start + length - 1}` };
    const response = await call(b, "GET", upload.key, {}, { headers: range });
    return new Uint8Array(await response.arrayBuffer());
  }, size).catch(() => undefined);
  const manifest = {
    key: upload.key,
    link: link.id,
    name: upload.name,
    // The key's folder, which a resumed upload keeps even if the kind changed.
    stream: upload.key.split("/")[2],
    filename: upload.filename,
    type: upload.type,
    size,
    seconds: seconds ?? null,
    lastModified: new Date(upload.lastModified).toISOString(),
    uploadedAt: new Date().toISOString(),
    country: request.headers.get("cf-ipcountry"),
    userAgent: request.headers.get("user-agent"),
  };
  await call(
    b,
    "PUT",
    manifestKey,
    {},
    {
      body: `${JSON.stringify(manifest, null, 2)}\n`,
      headers: { "content-type": "application/json" },
    },
  );
  return { seconds: seconds ?? null, size };
}

const escape = (s: string) =>
  s
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
