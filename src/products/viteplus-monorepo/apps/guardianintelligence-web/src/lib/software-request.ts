export const SOFTWARE_REQUEST_EVENT = "company.software_request_submitted";
export const SOFTWARE_REQUEST_ENDPOINT = "/api/events/guardian.analytics.v1.EventService/Publish";

export type SoftwareKind = "binary" | "service";

export interface SoftwareRequestInput {
  readonly company: string;
  readonly email: string;
  readonly request: string;
  readonly softwareKind: SoftwareKind;
}

export type SoftwareRequestField = keyof SoftwareRequestInput;
export type SoftwareRequestErrors = Partial<Record<SoftwareRequestField, string>>;

export type SoftwareRequestValidation =
  | { readonly ok: true; readonly value: SoftwareRequestInput }
  | { readonly ok: false; readonly errors: SoftwareRequestErrors };

const byteLength = (value: string) => new TextEncoder().encode(value).byteLength;

function requestPropsJson(requestId: string, input: SoftwareRequestInput) {
  return JSON.stringify({
    request_id: requestId,
    company: input.company,
    contact_email: input.email,
    software_kind: input.softwareKind,
    request: input.request,
  });
}

export function validateSoftwareRequest(input: SoftwareRequestInput): SoftwareRequestValidation {
  const value: SoftwareRequestInput = {
    company: input.company.trim(),
    email: input.email.trim().toLowerCase(),
    request: input.request.trim(),
    softwareKind: input.softwareKind,
  };
  const errors: SoftwareRequestErrors = {};

  if (value.company.length < 2) errors.company = "Enter the company you work with.";
  else if (byteLength(value.company) > 120)
    errors.company = "Keep the company name under 120 bytes.";

  if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.email)) {
    errors.email = "Enter a work email we can reply to.";
  } else if (byteLength(value.email) > 254) {
    errors.email = "Keep the email address under 254 bytes.";
  }

  if (value.request.length < 10) {
    errors.request = "Tell us a little more about what the software should do.";
  } else if (byteLength(value.request) > 1_000) {
    errors.request = "Keep the request under 1,000 bytes.";
  }

  if (value.softwareKind !== "binary" && value.softwareKind !== "service") {
    errors.softwareKind = "Choose a distributable binary or a hosted service.";
  }

  if (
    Object.keys(errors).length === 0 &&
    byteLength(requestPropsJson("00000000-0000-4000-8000-000000000000", value)) > 2_048
  ) {
    errors.request = "Use less punctuation or shorten the request so we can save it safely.";
  }

  return Object.keys(errors).length === 0 ? { ok: true, value } : { ok: false, errors };
}

export function softwareRequestPayload(
  requestId: string,
  input: SoftwareRequestInput,
  context: { readonly path: string; readonly referrer: string; readonly sentAtUnixMs: number },
) {
  const propsJson = requestPropsJson(requestId, input);
  if (byteLength(propsJson) > 2_048) {
    throw new Error("Software request exceeds the storage limit.");
  }
  return {
    sentAtUnixMs: String(context.sentAtUnixMs),
    events: [
      {
        name: SOFTWARE_REQUEST_EVENT,
        path: context.path,
        ...(context.referrer === "" ? {} : { referrer: context.referrer }),
        propsJson,
      },
    ],
  };
}

function acceptedOne(value: unknown): boolean {
  if (typeof value !== "object" || value === null) return false;
  const result = value as { accepted?: unknown; rejected?: unknown };
  return Number(result.accepted) === 1 && Number(result.rejected ?? 0) === 0;
}

export async function submitSoftwareRequest(input: SoftwareRequestInput): Promise<string> {
  const requestId = crypto.randomUUID();
  const payload = softwareRequestPayload(requestId, input, {
    path: window.location.pathname,
    referrer: document.referrer,
    sentAtUnixMs: Date.now(),
  });
  const response = await fetch(SOFTWARE_REQUEST_ENDPOINT, {
    method: "POST",
    headers: { "content-type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify(payload),
  });
  const result: unknown = await response.json().catch(() => null);
  if (!response.ok || !acceptedOne(result)) {
    throw new Error("The request inbox did not accept this submission.");
  }
  return requestId;
}
