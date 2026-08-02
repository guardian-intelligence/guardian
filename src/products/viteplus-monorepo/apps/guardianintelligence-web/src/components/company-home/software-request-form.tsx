import { useRef, useState, type FormEvent } from "react";
import { landing } from "~/content/landing";
import { emitSpan } from "~/lib/telemetry/browser";
import {
  submitSoftwareRequest,
  validateSoftwareRequest,
  type SoftwareKind,
  type SoftwareRequestErrors,
} from "~/lib/software-request";

type SubmissionState =
  | { readonly kind: "idle" }
  | { readonly kind: "submitting" }
  | { readonly kind: "success"; readonly requestId: string }
  | { readonly kind: "error"; readonly message: string };

function formValue(data: FormData, name: string): string {
  const value = data.get(name);
  return typeof value === "string" ? value : "";
}

function FieldError({
  id,
  message,
}: {
  readonly id: string;
  readonly message: string | undefined;
}) {
  if (!message) return null;
  return (
    <span id={id} className="software-request__field-error">
      {message}
    </span>
  );
}

export function SoftwareRequestForm() {
  const [submission, setSubmission] = useState<SubmissionState>({ kind: "idle" });
  const [errors, setErrors] = useState<SoftwareRequestErrors>({});
  const started = useRef(false);
  const busy = submission.kind === "submitting";

  const onStart = () => {
    if (started.current) return;
    started.current = true;
    emitSpan("company.software_request_started", {});
  };

  const onSubmit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    const candidate = {
      company: formValue(data, "company"),
      email: formValue(data, "email"),
      request: formValue(data, "request"),
      softwareKind: formValue(data, "softwareKind") as SoftwareKind,
    };
    const validation = validateSoftwareRequest(candidate);
    if (!validation.ok) {
      setErrors(validation.errors);
      setSubmission({ kind: "error", message: "Check the highlighted fields and try again." });
      emitSpan("company.software_request_validation_failed", {
        fields: Object.keys(validation.errors).sort().join(","),
      });
      return;
    }

    setErrors({});
    setSubmission({ kind: "submitting" });
    try {
      const requestId = await submitSoftwareRequest(validation.value);
      form.reset();
      setSubmission({ kind: "success", requestId });
      emitSpan("company.software_request_received", {
        request_id: requestId,
        software_kind: validation.value.softwareKind,
      });
    } catch {
      setSubmission({
        kind: "error",
        message: "We could not save this request. Your text is still here; please try again.",
      });
      emitSpan("company.software_request_failed", {});
    }
  };

  return (
    <form
      className="software-request"
      onFocusCapture={onStart}
      onSubmit={(event) => void onSubmit(event)}
      data-illumination-glass="panel"
      aria-describedby="software-request-note software-request-status"
      noValidate
    >
      <span className="software-request__corners" aria-hidden="true" />
      <div className="software-request__body">
        <label className="software-request__idea">
          <span className="software-request__label">What should exist?</span>
          <textarea
            name="request"
            rows={5}
            maxLength={1_000}
            placeholder={landing.requestPlaceholder}
            aria-invalid={Boolean(errors.request)}
            aria-describedby={errors.request ? "software-request-error" : undefined}
            disabled={busy}
            required
            data-illumination-glass="field"
          />
          <FieldError id="software-request-error" message={errors.request} />
        </label>

        <div className="software-request__identity">
          <label>
            <span className="software-request__label">Company</span>
            <input
              name="company"
              type="text"
              autoComplete="organization"
              maxLength={120}
              placeholder="Acme, Inc."
              aria-invalid={Boolean(errors.company)}
              aria-describedby={errors.company ? "software-company-error" : undefined}
              disabled={busy}
              required
              data-illumination-glass="field"
            />
            <FieldError id="software-company-error" message={errors.company} />
          </label>
          <label>
            <span className="software-request__label">Work email</span>
            <input
              name="email"
              type="email"
              inputMode="email"
              autoComplete="email"
              maxLength={254}
              placeholder="you@company.com"
              aria-invalid={Boolean(errors.email)}
              aria-describedby={errors.email ? "software-email-error" : undefined}
              disabled={busy}
              required
              data-illumination-glass="field"
            />
            <FieldError id="software-email-error" message={errors.email} />
          </label>
        </div>

        <fieldset className="software-request__kind">
          <legend className="software-request__label">Delivery</legend>
          <div className="software-request__kind-options">
            <label data-illumination-glass="control">
              <input
                type="radio"
                name="softwareKind"
                value="binary"
                defaultChecked
                disabled={busy}
              />
              <span>
                <strong>Distributable binary</strong>
                <small>A CLI, desktop app, library, or package.</small>
              </span>
            </label>
            <label data-illumination-glass="control">
              <input type="radio" name="softwareKind" value="service" disabled={busy} />
              <span>
                <strong>Hosted service</strong>
                <small>An API, web app, integration, or managed system.</small>
              </span>
            </label>
          </div>
          <FieldError id="software-kind-error" message={errors.softwareKind} />
        </fieldset>

        <div className="software-request__footer">
          <p id="software-request-note">{landing.openSourceNote}</p>
          <button type="submit" disabled={busy} data-illumination-glass="control">
            {busy ? "Sending…" : "Send request"}
            <span aria-hidden="true">↗</span>
          </button>
        </div>

        <div
          id="software-request-status"
          className={`software-request__status software-request__status--${submission.kind}`}
          role="status"
          aria-live="polite"
        >
          {submission.kind === "success"
            ? `Received. We will reply by email. Reference ${submission.requestId.slice(0, 8)}.`
            : submission.kind === "error"
              ? submission.message
              : ""}
        </div>
      </div>
    </form>
  );
}
