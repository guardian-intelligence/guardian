import { createHash, createHmac } from "node:crypto";

// Just enough S3 for multipart uploads to R2: SigV4 query-string presigning
// (path-style, region "auto", unsigned payload). The server signs its own
// calls the same way it signs the phones' part PUTs, so there is one signer.

export interface Bucket {
  readonly endpoint: string;
  readonly bucket: string;
  readonly accessKeyId: string;
  readonly secretAccessKey: string;
}

export class S3Error extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(`S3 ${status} ${code}`);
  }
}

// RFC 3986 unreserved characters stay; everything else is percent-encoded.
const encode = (value: string) =>
  encodeURIComponent(value).replace(
    /[!'()*]/g,
    (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`,
  );

const hmac = (key: string | Buffer, data: string) =>
  createHmac("sha256", key).update(data).digest();

export function presign(
  b: Bucket,
  method: string,
  key: string,
  query: Readonly<Record<string, string>>,
  expiresSeconds: number,
  now = new Date(),
): string {
  const { host, origin } = new URL(b.endpoint);
  const path = `/${b.bucket}/${key.split("/").map(encode).join("/")}`;
  const amzDate = now.toISOString().replace(/[-:]|\.\d{3}/g, "");
  const day = amzDate.slice(0, 8);
  const scope = `${day}/auto/s3/aws4_request`;
  const canonicalQuery = Object.entries({
    ...query,
    "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
    "X-Amz-Credential": `${b.accessKeyId}/${scope}`,
    "X-Amz-Date": amzDate,
    "X-Amz-Expires": String(expiresSeconds),
    "X-Amz-SignedHeaders": "host",
  })
    .map(([k, v]) => `${encode(k)}=${encode(v)}`)
    .sort()
    .join("&");
  const canonicalRequest = [
    method,
    path,
    canonicalQuery,
    `host:${host}\n`,
    "host",
    "UNSIGNED-PAYLOAD",
  ].join("\n");
  const stringToSign = [
    "AWS4-HMAC-SHA256",
    amzDate,
    scope,
    createHash("sha256").update(canonicalRequest).digest("hex"),
  ].join("\n");
  const signingKey = ["auto", "s3", "aws4_request"].reduce<Buffer>(
    (k, part) => hmac(k, part),
    hmac(`AWS4${b.secretAccessKey}`, day),
  );
  const signature = createHmac("sha256", signingKey).update(stringToSign).digest("hex");
  return `${origin}${path}?${canonicalQuery}&X-Amz-Signature=${signature}`;
}

// One signed server-side call; non-2xx answers throw with S3's error code.
export async function call(
  b: Bucket,
  method: string,
  key: string,
  query: Readonly<Record<string, string>> = {},
  init: { body?: string; headers?: Record<string, string> } = {},
): Promise<Response> {
  const response = await fetch(presign(b, method, key, query, 300), { method, ...init });
  if (!response.ok) {
    const body = method === "HEAD" ? "" : await response.text();
    throw new S3Error(response.status, xmlText(body, "Code") ?? "Unknown");
  }
  return response;
}

export function xmlText(xml: string, tag: string): string | undefined {
  const raw = new RegExp(`<${tag}>([^<]*)</${tag}>`).exec(xml)?.[1];
  return raw === undefined ? undefined : unescapeXml(raw);
}

// ETags come back quoted and entity-encoded (&quot; or &#34;, by server).
const ENTITIES: Readonly<Record<string, string>> = {
  amp: "&",
  lt: "<",
  gt: ">",
  quot: '"',
  apos: "'",
};
const unescapeXml = (s: string) =>
  s.replace(/&(#x[0-9a-f]+|#\d+|[a-z]+);/gi, (entity, name: string) =>
    name.startsWith("#x") || name.startsWith("#X")
      ? String.fromCodePoint(parseInt(name.slice(2), 16))
      : name.startsWith("#")
        ? String.fromCodePoint(Number(name.slice(1)))
        : (ENTITIES[name] ?? entity),
  );

export function xmlAll(xml: string, tag: string): string[] {
  return [...xml.matchAll(new RegExp(`<${tag}>([\\s\\S]*?)</${tag}>`, "g"))].map((m) => m[1]!);
}
