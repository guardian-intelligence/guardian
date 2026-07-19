# GitHub login-canary machine account

Use this procedure once to create the permanent, unprivileged GitHub user used
by the Sign in with Guardian browser canary.

GitHub permits a machine account only when an individual creates it, accepts
the terms, supplies a valid email address, and remains responsible for the
account. Automated signup is not permitted. A computer-use agent may prepare
and verify this procedure, but it must not submit the signup form.

Sources of authority:

- [GitHub Terms of Service](https://docs.github.com/en/site-policy/github-terms/github-terms-of-service)
- [GitHub machine-user guidance](https://docs.github.com/en/authentication/connecting-to-github-with-ssh/managing-deploy-keys#machine-users)
- [Creating a GitHub account](https://docs.github.com/en/get-started/start-your-journey/creating-an-account-on-github)
- [Configuring TOTP two-factor authentication](https://docs.github.com/en/authentication/securing-your-account-with-two-factor-authentication-2fa/configuring-two-factor-authentication)

## Input template

Complete this record in the secure custody workspace before opening GitHub:

```text
ACCOUNT_PURPOSE=Sign in with Guardian full browser canary
ACCOUNT_OWNER=<responsible human>
ACCOUNT_EMAIL=<dedicated, monitored, valid email address>
ACCOUNT_USERNAME=<dedicated GitHub username; availability is checked at signup>
PASSWORD_RECORD=<password-manager or custody record identifier>
```

Generate a unique password of at least 32 characters in the password manager.
Do not put the password itself in this template.

## Procedure

1. The agent opens `https://github.com/signup` in the clean browser profile and
   confirms that the URL is on `github.com`.
2. **Human gate:** The accountable human takes control for the entire signup,
   enters the template values and generated password, accepts GitHub's terms,
   completes any challenge, and verifies the email address.
3. The agent resumes after the human confirms that the account is signed in.
   Open **Settings → Password and authentication**.
4. **Human gate:** Enable two-factor authentication with a TOTP authenticator.
   The human records the TOTP setup key and recovery codes directly into the
   custody record. Recovery codes must not be projected into Kubernetes.
5. Review the account:
   - it is not a member, owner, or outside collaborator of any organization;
   - it has no repository access, personal access token, OAuth token, SSH key,
     GPG key, billing method, or GitHub App installation;
   - its public profile does not identify an individual;
   - its email remains monitored for security and terms notifications.
6. Sign out. In another fresh browser profile, sign in using the username,
   password, and one current TOTP code. Sign out again.
7. Close the browser profiles and clear any downloads or clipboard contents
   created during the run.

## Custody output

The production custody import accepts these three values:

```text
PROD_GITHUB_LOGIN_CANARY_USERNAME=<ACCOUNT_USERNAME>
PROD_GITHUB_LOGIN_CANARY_PASSWORD=<generated password>
PROD_GITHUB_LOGIN_CANARY_TOTP_SECRET=<TOTP setup key>
```

They are imported to:

```text
guardian/guardian-mgmt/tenant-guardian-prod/keycloak/login-canary-github
```

with the properties `username`, `password`, and `totp_secret`. Keep the
recovery codes and accountable-owner record in custody only.

Follow
[`src/infrastructure/runbooks/openbao-static-seal-self-init.md`](../src/infrastructure/runbooks/openbao-static-seal-self-init.md)
for the custody import ceremony. Wipe the restored custody workspace
immediately after a successful import.

## Acceptance record

```text
created_by_human=<name>
created_at=<RFC3339 timestamp>
email_verified=yes
totp_enabled=yes
fresh_profile_password_and_totp_login=pass
organization_memberships=0
repository_access=0
tokens_and_keys=0
custody_record=<identifier>
```

Stop and escalate if any value differs from the expected result. Do not weaken
the account or canary to work around a failed check.
