---
name: create-github-login-canary
description: Create and secure the GitHub machine account used by the Sign in with Guardian full-browser OAuth canary. Use when provisioning or replacing the login-canary GitHub user, avatar, password, TOTP seed, recovery codes, and OpenBao values.
---

1. Set `EMAIL` to the operator's existing deliverable address with
   `+guardian-login-canary` inserted before `@`; set `USERNAME` to an
   available Guardian-specific login-canary name; generate a unique
   32-character-or-longer password.
2. Generate `/tmp/guardian-login-canary.png` as a 512×512 PNG on Guardian
   ink black (see `https://guardianintelligence.org/design` for the full
   design packet). Use the Guardian wings logo in white on ink black. Add a
   slightly rounded flare-colored box with ink-black Geist text reading
   `C-01` (for "canary 01").
3. Open `https://github.com/signup` in a fresh browser profile and create the
   account with `EMAIL`, `USERNAME`, and the generated password.
4. Verify `EMAIL`, open the account profile, and upload `/tmp/guardian-login-canary.png` as the avatar.
5. Open **Settings → Password and authentication**, enable TOTP two-factor authentication, and capture the TOTP setup key and recovery codes.
6. Remove every organization membership, repository grant, token, SSH key, GPG key, billing method, and GitHub App installation from the account.
7. Sign out, then prove a fresh-profile login with `USERNAME`, the password,
   and a current TOTP code.
8. In that fresh profile, open `https://guardianintelligence.org/postflight`,
   choose **Sign in with GitHub** (the browser goes straight to github.com —
   no Keycloak page renders), approve **Sign in with Guardian** once, land on
   the Postflight console, and verify the App is listed under GitHub's
   **Settings → Applications → Authorized OAuth Apps**.
9. Write `USERNAME`, the password, the TOTP setup key, and the recovery codes
   straight to OpenBao at
   `guardian/guardian-mgmt/tenant-guardian-prod/keycloak/login-canary-github`
   with a namespace-scoped `secrets-writer` token, values on stdin — see
   `docs/secrets.md`, "Adding a secret for a third-party integration". Every
   one of them is reissuable from GitHub, so none passes through custody.
   Never transmit credentials or recovery codes in chat.
