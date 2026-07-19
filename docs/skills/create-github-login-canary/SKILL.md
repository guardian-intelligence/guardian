---
name: create-github-login-canary
description: Create and secure the GitHub machine account used by the Sign in with Guardian full-browser OAuth canary. Use when provisioning or replacing the login-canary GitHub user, avatar, password, TOTP seed, recovery codes, and custody values.
---

1. Set `EMAIL` to the operator's existing deliverable address with
   `+guardian-login-canary` inserted before `@`; set `USERNAME` to an
   available Guardian-specific login-canary name; generate a unique
   32-character-or-longer password.
2. Generate `/tmp/guardian-login-canary.png` as a 512×512 PNG on `#0E0E0E`.
   Use
   `src/products/viteplus-monorepo/apps/guardianintelligence-web/public/favicon.svg`
   as the immutable mark source; preserve its exact viewBox, wings path, and
   aspect ratio. Render it 320×320, centered at `(256,186)`, without
   nonuniform scaling. Add `LOGIN CANARY` centered below it in uppercase
   white Geist Semibold with `0.14em` tracking. Verify the spelling and square
   dimensions.
3. Open `https://github.com/signup` in a fresh browser profile and create the
   account with `EMAIL`, `USERNAME`, and the generated password.
4. Verify `EMAIL`, open the account profile, and upload
   `/tmp/guardian-login-canary.png` as the avatar.
5. Open **Settings → Password and authentication**, enable TOTP two-factor
   authentication, and capture the TOTP setup key and recovery codes.
6. Remove every organization membership, repository grant, token, SSH key,
   GPG key, billing method, and GitHub App installation from the account.
7. Sign out, then prove a fresh-profile login with `USERNAME`, the password,
   and a current TOTP code.
8. Store `USERNAME`, the password, and the TOTP setup key as
   `PROD_GITHUB_LOGIN_CANARY_USERNAME`,
   `PROD_GITHUB_LOGIN_CANARY_PASSWORD`, and
   `PROD_GITHUB_LOGIN_CANARY_TOTP_SECRET` in custody. Store recovery codes
   only in custody.
9. Import the three canary values to
   `guardian/guardian-mgmt/tenant-guardian-prod/keycloak/login-canary-github`
   and wipe the restored custody workspace immediately.
