# Refresh the ip2asn snapshot

The analytics ingest image bakes in a BGP-derived IP→ASN range snapshot
(iptoasn.com combined TSV — public routing data, no license restrictions).
It is mirrored as a release on this repo because the upstream URL is
mutable, and pinned by sha256 in `MODULE.bazel` (`ip2asn_combined`).

ASN churn is slow; refresh roughly monthly, or when an abuse investigation
hits visibly stale attributions. Second occurrence: automate this as a bot
PR per the repo automation rule.

## Execute

```bash
DATE=$(date +%Y%m%d)
curl -sLo ip2asn-combined.tsv.gz https://iptoasn.com/data/ip2asn-combined.tsv.gz
sha256sum ip2asn-combined.tsv.gz
gh release create "data-ip2asn-${DATE}" ip2asn-combined.tsv.gz \
  -R guardian-intelligence/guardian \
  --title "ip2asn snapshot ${DATE}" --latest=false \
  --notes "Unmodified mirror of https://iptoasn.com/data/ip2asn-combined.tsv.gz fetched ${DATE}. sha256: <paste>. Consumed by MODULE.bazel ip2asn_combined; refresh: src/infrastructure/runbooks/ip2asn-refresh.md"
```

Then, in a normal PR, point the `ip2asn_combined` `http_file` in
`MODULE.bazel` at the new release URL and sha256. Merging rebuilds and
ships the ingest image through the usual image path (build → push → digest
bump in `deployments/analytics/system/ingest.yaml`).

## Verify

- `bazelisk test //src/products/analytics/ingest:ingest_test` passes (the
  loader rejects malformed snapshots at startup, so a bad file fails the
  image before it ships).
- Post-deploy: ingest logs `ip2asn table loaded` with a plausible range
  count (~450k+), and `analytics_ingest_events_total{asn="present"}`
  keeps growing.

Keep the two most recent data releases; delete older ones so the release
list stays navigable.
