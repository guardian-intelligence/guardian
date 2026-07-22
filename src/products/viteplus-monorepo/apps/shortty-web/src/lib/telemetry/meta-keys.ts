// Meta-tag names bridging SSR-known deploy attributes into the served HTML.
// The company service reads GUARDIAN_SITE, GUARDIAN_DEPLOY_*, and
// GUARDIAN_SUPERVISOR from the Kubernetes runtime env and head() emits these
// meta tags (server-deploy-meta.ts, the writer) so client-side consumers —
// the analytics beacon — can stamp deploy context onto events.

export const DEPLOY_META = {
  site: "guardian:site",
  runKey: "guardian:deploy-run-key",
  id: "guardian:deploy-id",
  commitSha: "guardian:commit-sha",
  supervisor: "guardian:supervisor",
} as const;
