import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import * as v from "valibot";
import { validUserCode } from "~/lib/postflight-auth";
import "~/styles/postflight.css";

// A hand-edited or truncated link degrades to the empty form instead of an
// error page.
const deviceSearchSchema = v.fallback(
  v.object({
    user_code: v.optional(v.string()),
    error: v.optional(v.string()),
  }),
  {},
);

export const Route = createFileRoute("/postflight/device")({
  validateSearch: (search) => v.parse(deviceSearchSchema, search),
  component: DeviceApprovalPage,
});

function DeviceMark() {
  return (
    <svg aria-hidden="true" className="postflight-mark postflight-mark--small" viewBox="0 0 44 44">
      <path d="M22 4.5a17.5 17.5 0 1 0 17.5 17.5" />
      <path d="M14 24.5 21 31l15-19" />
      <circle cx="36" cy="12" r="4" />
    </svg>
  );
}

function DeviceApprovalPage() {
  const search = Route.useSearch();
  const initialCode = validUserCode(search.user_code) ?? "";
  const [code, setCode] = useState(initialCode);
  const validCode = validUserCode(code);
  const approveHref = validCode
    ? `/postflight/auth/login?return_to=${encodeURIComponent(
        `/postflight/device/continue?user_code=${encodeURIComponent(validCode)}`,
      )}`
    : undefined;

  return (
    <main className="postflight-device-page">
      <div className="postflight-login-card postflight-device-card">
        <div className="postflight-card-logo">
          <DeviceMark />
        </div>
        <h2>Approve CLI sign-in</h2>
        {search.error === "expired" ? (
          <p className="postflight-device-error" data-device-error>
            That code expired. Run <code>postflight auth login</code> again and enter the new code.
          </p>
        ) : null}
        <p>
          A terminal running the postflight CLI is asking to sign in as you. Check that this code
          matches the one your terminal printed:
        </p>
        <label className="postflight-device-code-label" htmlFor="device-user-code">
          One-time code
        </label>
        <input
          id="device-user-code"
          className="postflight-device-code"
          autoComplete="off"
          spellCheck={false}
          inputMode="text"
          dir="ltr"
          value={code}
          onChange={(event) => setCode(event.target.value)}
        />
        <a
          id="postflight-device-approve"
          aria-disabled={!approveHref}
          className={`postflight-guardian-button${approveHref ? "" : " postflight-guardian-button--disabled"}`}
          href={approveHref}
        >
          Approve with GitHub
        </a>
        <p className="postflight-device-caution">
          Only approve codes from a command you just ran yourself. If someone sent you this link or
          asked you to enter a code for them, close this page — approving would give their terminal
          access to your account.
        </p>
      </div>
    </main>
  );
}
