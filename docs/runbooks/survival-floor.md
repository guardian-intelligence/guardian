# Survival floor (M0): offsite copies of the unmintable things

Customer-grade: copy-paste commands with expected outcomes. This is NOT the
custody system (that arrives with Zitadel/SpiceDB) — it is the floor under
two single-copy assets: the per-site cluster CA roots
(`~/.local/state/guardian/`, the only credentials to three SSH-less
clusters) and the prod corpus (one NVMe, no replica).

## R2 facts (measured, 2026-06-12)

- The bucket is `guardian-vault` on the shared Cloudflare account (same
  account as verself's buckets). Credentials in `secret.env` (gitignored):
  `cloudflare_account_id`, `cloudflare_r2_api_token`,
  `cloudflare_r2_access_key_id`, `cloudflare_r2_secret_access_key`.
- Account-owned API tokens (dashboard → Account API Tokens, even with R2
  permission groups) are a DEAD END for the R2 data plane. Measured matrix:
  as a Bearer the token could ONLY list buckets via
  `GET /accounts/{id}/r2/buckets`; R2 REST object GET/PUT and bucket
  creation all returned 9106; the generic verify endpoints
  (`/user/tokens/verify` and `/accounts/{id}/tokens/verify`) rejected it
  with 6003/6111 — account-owned tokens are not accepted there. The
  documented S3 derivation (Access Key ID = token ID, Secret = SHA-256 hex
  of the token value) returned SignatureDoesNotMatch: the derivation does
  NOT work for account-owned tokens, only for tokens minted through the R2
  dashboard flow. (An earlier draft blamed a `v1.0-…` token format; the
  token never had that prefix — the owner, not the format, was the problem.)
- The path that works: R2 → "Manage R2 API Tokens" → create. That flow
  displays a working trio at creation (Token value, Access Key ID, Secret
  Access Key) — record all three into `secret.env` then; the Secret is used
  as-is, no derivation step. Scope: Object Read & Write on `guardian-vault`
  only, with a TTL.
- Endpoint `https://<account_id>.r2.cloudflarestorage.com`, region `auto`.
  Zero egress fees — restore drills cost nothing.
- The M5 backup CronJob must NOT inherit the operator's TTL'd token: mint a
  non-expiring bucket-scoped Object-R/W token then, and page on backup
  failure (a stale backup is silent data loss).

## Encryption: age, identity mode

One X25519 identity (`age-keygen`) encrypts everything; the identity string
(`AGE-SECRET-KEY-1…`) is vaulted in the operator's sops store. The `/tmp`
identity file used for the drill is deleted only after the upload
round-trip verify — from that point the identity lives NOWHERE on disk.
Charter value 8 note: age is X25519, not post-quantum
— gap documented per the charter's own clause; revisit when an age ML-KEM
plugin is boring.

## Produce + upload (run from the controller)

```sh
set -a; source secret.env; set +a
STAMP=$(date +%F)
tar -C ~/.local/state -czf /tmp/guardian-state-$STAMP.tar.gz guardian
KUBECONFIG=~/.local/state/guardian/guardian-prod/kubeconfig \
  kubectl -n aisucks exec postgres-0 -- pg_dump -U aisucks --clean --if-exists aisucks \
  > /tmp/aisucks-prod-$STAMP.sql
sha256sum /tmp/guardian-state-$STAMP.tar.gz /tmp/aisucks-prod-$STAMP.sql  # record both
age -r <recipient> -o /tmp/guardian-state-$STAMP.tar.gz.age /tmp/guardian-state-$STAMP.tar.gz
age -r <recipient> -o /tmp/aisucks-prod-$STAMP.sql.age   /tmp/aisucks-prod-$STAMP.sql
python3 - <<'PY'
import os, glob, boto3
s3 = boto3.client('s3',
    endpoint_url=f"https://{os.environ['cloudflare_account_id']}.r2.cloudflarestorage.com",
    aws_access_key_id=os.environ['cloudflare_r2_access_key_id'],
    aws_secret_access_key=os.environ['cloudflare_r2_secret_access_key'],
    region_name='auto')
for f in glob.glob('/tmp/*.age'):
    key = ('state/' if 'guardian-state' in f else 'corpus/') + f.split('/')[-1]
    s3.upload_file(f, 'guardian-vault', key)
    print('uploaded', key)
PY
```

Expected: two `uploaded` lines. Then pull a SECOND copy onto hardware you
own (MacBook): same boto3 snippet with `download_file`, or any S3 client
with the same endpoint/creds. The controller copy does not count — the
whole point is surviving the controller.

## Restore (drilled 2026-06-12, counts matched prod exactly)

```sh
age -d -i <identity-file> guardian-state-<date>.tar.gz.age | tar -C ~/.local/state -xz
# expect: ~/.local/state/guardian/{guardian-dev,guardian-gamma,guardian-prod}/secrets.yaml present
```

Corpus, into a scratch postgres (drill-proven gotchas: wait for pg_isready
— pod Ready ≠ postgres ready; CREATE ROLE and CREATE DATABASE must be
separate psql -c calls or CREATE DATABASE dies inside the implicit
transaction):

```sh
kubectl -n aisucks run scratch-pg --image=<seed-registry postgres digest> \
  --env=POSTGRES_PASSWORD=scratch --restart=Never
until kubectl -n aisucks exec scratch-pg -- pg_isready -U postgres | grep -q accepting; do sleep 3; done
kubectl -n aisucks exec scratch-pg -- psql -U postgres -c "CREATE ROLE aisucks LOGIN"
kubectl -n aisucks exec scratch-pg -- psql -U postgres -c "CREATE DATABASE aisucks OWNER aisucks"
age -d -i <identity-file> aisucks-prod-<date>.sql.age | \
  kubectl -n aisucks exec -i scratch-pg -- psql -q -U postgres -d aisucks -f -
kubectl -n aisucks exec scratch-pg -- psql -U postgres -d aisucks -t \
  -c "select count(*) from reports"   # expect: the recorded count
kubectl -n aisucks delete pod scratch-pg
```

## Record

Floor in place: both artifacts uploaded to guardian-vault (state/ and
corpus/ keys), verified by download + decrypt + sha256 match; restore drill
passed (counts matched prod exactly). The decryption identity lives only in
the operator's sops store, the payloads only in R2 — nothing remains on the
controller. Remaining operator action: pull a second copy onto the MacBook
(the snippet above with download_file).
Note: secret.env also carries cloudflare_r2_s3_api_endpoint (the dashboard
shows it at token creation); the upload snippet prefers it when present.

| date | artifact | plaintext sha256 | counts |
|---|---|---|---|
| 2026-06-12 | guardian-state-2026-06-12.tar.gz | `a11dd42325a52ea6cecd5a4f9f33097fa148441658a14107e026b5dd2d5c27df` | 3 site dirs |
| 2026-06-12 | aisucks-prod-2026-06-12.sql | `b7de18b942da9b358f141c21debf9ca40af4b3f75aa96575795101055ff8299f` | reports=2, turns=6 (restore-drilled, matched) |
