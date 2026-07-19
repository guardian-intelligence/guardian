# Computer-use instructions

These runbooks describe browser operations that are intentionally outside the
cluster control plane. They are written as templates so an operator can give
the same procedure to a computer-use agent in production or staging without
copying secrets into a prompt.

| Procedure | Purpose |
| - | - |
| [GitHub login-canary machine account](github-login-canary-machine-account.md) | Create and secure the unprivileged GitHub user that exercises the complete Sign in with Guardian browser flow. |
| [Sign in with Guardian OAuth App](sign-in-with-guardian-oauth-app.md) | Create or update the GitHub social-login registration for one Guardian environment. |
| [Postflight GitHub App](postflight-github-app.md) | Create, secure, and install the GitHub App that receives Postflight jobs in one environment. |

## Rules for every run

- Start in a fresh browser profile with screen recording, browser sync,
  extensions, clipboard history, and prompt logging disabled.
- Never place a password, client secret, webhook secret, TOTP setup key,
  recovery code, or private key in chat, screenshots, issue text, commit text,
  or the repository.
- The agent must pause for any step marked **Human gate** and whenever GitHub
  presents a CAPTCHA, terms acceptance, recovery-code display, or unexpected
  privilege request.
- A run is not complete until its acceptance checks and output record are
  filled in. Store the output record with the custody material, not in Git.
- Production has no display-name suffix. Staging uses the literal suffix
  ` (Staging)`. Beta and gamma are not supported application environments.
