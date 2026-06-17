// Meta-tag names used to bridge SSR-known deploy attributes onto the browser
// OTel resource. The company service reads GUARDIAN_SITE, GUARDIAN_DEPLOY_*,
// and GUARDIAN_SUPERVISOR from the Kubernetes runtime env, head() emits these
// meta tags into the SSR HTML, and browser.ts reads them on init so every span
// carries the right `guardian.*` ResourceAttributes.
//
// Keep names in sync between server-deploy-meta.ts (writer) and browser.ts (reader).

export const DEPLOY_META = {
  site: "guardian:site",
  runKey: "guardian:deploy-run-key",
  id: "guardian:deploy-id",
  commitSha: "guardian:commit-sha",
  supervisor: "guardian:supervisor",
} as const;

export const RESOURCE_ATTR_KEYS = {
  site: "guardian.site",
  runKey: "guardian.deploy_run_key",
  id: "guardian.deploy_id",
  commitSha: "guardian.commit_sha",
  supervisor: "guardian.supervisor",
} as const;
