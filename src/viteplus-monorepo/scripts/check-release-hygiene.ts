#!/usr/bin/env tsx
import readChangesets from "@changesets/read";
import { getPackages } from "@manypkg/get-packages";

const workspaceRoot = process.cwd();

type PublishablePackage = {
  version: string;
};

type PendingRelease = {
  changesetFile: string;
  packageName: string;
  packageVersion: string;
};

function parseRequestedPackages(args: readonly string[]): Set<string> {
  const requestedPackages = new Set<string>();

  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === "--package") {
      const packageName = args[i + 1];
      if (typeof packageName !== "string" || packageName === "" || packageName.startsWith("--")) {
        throw new Error("--package requires a package name");
      }
      requestedPackages.add(packageName);
      i += 1;
      continue;
    }
    throw new Error(`unknown argument: ${arg}`);
  }

  return requestedPackages;
}

async function publishableWorkspacePackages(): Promise<Map<string, PublishablePackage>> {
  const workspace = await getPackages(workspaceRoot);
  const publishablePackages = new Map<string, PublishablePackage>();

  for (const workspacePackage of workspace.packages) {
    const { packageJson } = workspacePackage;
    if (packageJson.private === true) {
      continue;
    }
    publishablePackages.set(packageJson.name, {
      version: packageJson.version,
    });
  }

  return publishablePackages;
}

const requestedPackages = parseRequestedPackages(process.argv.slice(2));
const publishablePackages = await publishableWorkspacePackages();
const targetPackages =
  requestedPackages.size > 0 ? requestedPackages : new Set(publishablePackages.keys());

for (const packageName of targetPackages) {
  if (!publishablePackages.has(packageName)) {
    throw new Error(`release hygiene target is not a publishable workspace package: ${packageName}`);
  }
}

const changesets = await readChangesets(workspaceRoot);
const pending: PendingRelease[] = [];

for (const changeset of changesets) {
  for (const release of changeset.releases) {
    if (release.type === "none" || !targetPackages.has(release.name)) {
      continue;
    }

    const publishablePackage = publishablePackages.get(release.name);
    if (publishablePackage === undefined) {
      continue;
    }

    pending.push({
      changesetFile: `.changeset/${changeset.id}.md`,
      packageName: release.name,
      packageVersion: publishablePackage.version,
    });
  }
}

if (pending.length > 0) {
  console.error(
    "unapplied publishable package Changesets remain; run `vp changeset:version` before release-ready checks:",
  );
  for (const item of pending) {
    console.error(`  ${item.changesetFile} -> ${item.packageName}@${item.packageVersion}`);
  }
  process.exit(1);
}

process.stdout.write("release hygiene ok\n");
