# @guardian-intelligence/postflight

The [Postflight](https://guardianintelligence.org/postflight) CLI. Warm,
isolated GitHub runners that turn CI from a queue into feedback.

```sh
npm i -g @guardian-intelligence/postflight
postflight version
```

The package carries no install script: npm picks the one platform package
whose `os`/`cpu` match the machine, and the `postflight` bin resolves the
binary out of it.

Every published binary is the byte-identical artifact attached to the
matching [GitHub Release](https://github.com/guardian-intelligence/guardian/releases),
signed at build time and verifiable without trusting this registry:

```sh
cosign verify-blob --bundle postflight-<target>.sigstore.json \
  --certificate-identity https://github.com/guardian-intelligence/guardian/.github/workflows/postflight-cli-image.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  postflight-<target>
```
