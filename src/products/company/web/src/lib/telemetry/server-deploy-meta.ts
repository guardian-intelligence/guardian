import { DEPLOY_META } from "./meta-keys";

export interface DeployMetaTag {
  readonly name: string;
  readonly content: string;
}

// Server-only reader. `import.meta.env.SSR` is replaced at build time so the
// process.env access is dead code on the client and tree-shaken away. Returns
// the head() meta-tag list to embed in SSR HTML, where browser.ts can read it.
export function deployMetaTags(): DeployMetaTag[] {
  if (!import.meta.env.SSR) {
    return [];
  }
  const env = process.env;
  const tags: DeployMetaTag[] = [
    { name: DEPLOY_META.site, content: env.GUARDIAN_SITE ?? "" },
    { name: DEPLOY_META.runKey, content: env.GUARDIAN_DEPLOY_RUN_KEY ?? "" },
    { name: DEPLOY_META.id, content: env.GUARDIAN_DEPLOY_ID ?? "" },
    { name: DEPLOY_META.commitSha, content: env.GUARDIAN_COMMIT_SHA ?? "" },
    { name: DEPLOY_META.supervisor, content: env.GUARDIAN_SUPERVISOR ?? "" },
  ];
  return tags.filter((tag) => tag.content !== "");
}
