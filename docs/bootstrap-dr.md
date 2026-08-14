# Bootstrap and disaster recovery

* Cloudflare bootstrap credentials: user must pay $10/mo to enable CloudFlare LB with 3 endpoints (1 for each ingress node). This is not enabled by default.
* Airgapped hermetically-sealed bootstrap done through the generated union images lock (declared + rendered, `//src/infrastructure/cmd/imageset`) + Rancher Hauler + Sidero Labs `talm` for Talos on bare metal soil (currently Latitude.sh).
* Cold-bootstrap trust model: the local checkout, its Bazel-built artifacts, the operator vault (Cloudflare account login, custody passphrase, age identity), and the R2-replicated custody artifacts (custody bundle, age-encrypted bootstrap set, OpenBao raft snapshots) are everything a from-nothing bring-up may require — see `docs/secrets.md`. Bootstrap-only compromises are allowed, but the cluster must converge to the declared steady state afterward.
