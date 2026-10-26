import { createHash, createHmac } from "node:crypto";

// Just enough S3 for multipart uploads to R2: SigV4 (path-style, region
// "auto", unsigned payload). The phones' part PUTs get presigned URLs; the
// server's own calls carry an Authorization header, since R2 documents
// presigning only for GET, HEAD, PUT and DELETE.

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

const canonicalQuery = (query: Readonly<Record<string, string>>) =>
  Object.entries(query)
    .map(([k, v]) => `${encode(k)}=${encode(v)}`)
    .sort()
    .join("&");

function scoped(b: Bucket, key: string, now: Date) {
  const { host, origin } = new URL(b.endpoint);
  const path = `/${b.bucket}/${key.split("/").map(encode).join("/")}`;
  const amzDate = now.toISOString().replace(/[-:]|\.\d{3}/g, "");
  const day = amzDate.slice(0, 8);
  const scope = `${day}/auto/s3/aws4_request`;
  const sign = (method: string, query: string, headers: Readonly<Record<string, string>>) => {
    const names = Object.keys(headers).sort();
    const canonicalRequest = [
      method,
      path,
      query,
      names.map((n) => `${n}:${headers[n]}\n`).join(""),
      names.join(";"),
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
    return createHmac("sha256", signingKey).update(stringToSign).digest("hex");
  };
  return { host, origin, path, amzDate, credential: `${b.accessKeyId}/${scope}`, sign };
}

export function presign(
  b: Bucket,
  method: string,
  key: string,
  query: Readonly<Record<string, string>>,
  expiresSeconds: number,
  now = new Date(),
): string {
  const { host, origin, path, amzDate, credential, sign } = scoped(b, key, now);
  const q = canonicalQuery({
    ...query,
    "X-Amz-Algorithm": "AWS4-HMAC-SHA256",
    "X-Amz-Credential": credential,
    "X-Amz-Date": amzDate,
    "X-Amz-Expires": String(expiresSeconds),
    "X-Amz-SignedHeaders": "host",
  });
  return `${origin}${path}?${q}&X-Amz-Signature=${sign(method, q, { host })}`;
}

// One signed server-side call; non-2xx answers throw with S3's error code.
export async function call(
  b: Bucket,
  method: string,
  key: string,
  query: Readonly<Record<string, string>> = {},
  init: { body?: string; headers?: Record<string, string> } = {},
): Promise<Response> {
  const { host, origin, path, amzDate, credential, sign } = scoped(b, key, new Date());
  const q = canonicalQuery(query);
  const signed = { host, "x-amz-content-sha256": "UNSIGNED-PAYLOAD", "x-amz-date": amzDate };
  const signature = sign(method, q, signed);
  const response = await fetch(`${origin}${path}${q ? `?${q}` : ""}`, {
    method,
    ...(init.body === undefined ? {} : { body: init.body }),
    headers: {
      ...init.headers,
      "x-amz-content-sha256": signed["x-amz-content-sha256"],
      "x-amz-date": amzDate,
      authorization: `AWS4-HMAC-SHA256 Credential=${credential}, SignedHeaders=${Object.keys(signed).sort().join(";")}, Signature=${signature}`,
    },
  });
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
