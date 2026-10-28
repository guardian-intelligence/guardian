import { getRequest } from "@tanstack/react-start/server";
import { readPostflightSession, type PostflightSession } from "~/lib/postflight-auth";

// Who may edit letters: Guardian accounts (the site's Keycloak sign-in, the
// same session Postflight uses) whose account id is listed in
// LETTERS_EDITOR_SUBJECTS. Account ids are opaque and stable; GitHub logins
// are re-registrable, so they never gate anything.
//
// LETTERS_EDITOR_SUBJECTS   comma-separated Keycloak `sub` values (web.yaml)

export type EditorAccess =
  | { readonly kind: "signed-out" }
  | { readonly kind: "not-editor"; readonly username: string; readonly subject: string }
  | { readonly kind: "editor"; readonly session: PostflightSession };

function editorSubjects(): ReadonlySet<string> {
  return new Set(
    (process.env["LETTERS_EDITOR_SUBJECTS"] ?? "")
      .split(",")
      .map((subject) => subject.trim())
      .filter(Boolean),
  );
}

export async function letterEditorAccess(): Promise<EditorAccess> {
  if (process.env["POSTFLIGHT_AUTH_ENABLED"] !== "true") return { kind: "signed-out" };
  const session = await readPostflightSession(getRequest());
  if (!session) return { kind: "signed-out" };
  if (!editorSubjects().has(session.subject)) {
    return { kind: "not-editor", username: session.username, subject: session.subject };
  }
  return { kind: "editor", session };
}
