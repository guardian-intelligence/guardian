#!/usr/bin/env node
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";

const workspaceRoot = process.cwd();
const changesetDir = path.join(workspaceRoot, ".changeset");
const packageGlobs = [
  "apps",
  "packages",
  "../products",
];

const requestedPackages = new Set();
for (let i = 2; i < process.argv.length; i += 1) {
  const arg = process.argv[i];
  if (arg === "--package") {
    const packageName = process.argv[i + 1];
    if (!packageName) {
      throw new Error("--package requires a package name");
    }
    requestedPackages.add(packageName);
    i += 1;
    continue;
  }
  throw new Error(`unknown argument: ${arg}`);
}

function readJson(file) {
  return JSON.parse(readFileSync(file, "utf8"));
}

function candidatePackageDirs() {
  const dirs = [];
  for (const base of packageGlobs) {
    const absoluteBase = path.resolve(workspaceRoot, base);
    try {
      for (const entry of readdirSync(absoluteBase)) {
        const entryPath = path.join(absoluteBase, entry);
        if (!statSync(entryPath).isDirectory()) {
          continue;
        }
        if (base === "../products") {
          const webPath = path.join(entryPath, "web");
          try {
            if (statSync(webPath).isDirectory()) {
              dirs.push(webPath);
            }
          } catch {
            // Product without a VitePlus web package.
          }
          continue;
        }
        dirs.push(entryPath);
      }
    } catch {
      // Workspace pattern with no matching directory.
    }
  }
  return dirs;
}

const publishablePackages = new Map();
for (const dir of candidatePackageDirs()) {
  const manifest = path.join(dir, "package.json");
  try {
    const pkg = readJson(manifest);
    if (pkg.name && pkg.private !== true) {
      publishablePackages.set(pkg.name, { dir, version: pkg.version ?? "0.0.0" });
    }
  } catch {
    // Candidate without package.json.
  }
}

const targetPackages = requestedPackages.size > 0 ? requestedPackages : new Set(publishablePackages.keys());
for (const packageName of targetPackages) {
  if (!publishablePackages.has(packageName)) {
    throw new Error(`release hygiene target is not a publishable workspace package: ${packageName}`);
  }
}

function changesetPackageBumps(file) {
  const text = readFileSync(file, "utf8");
  const lines = text.split(/\r?\n/);
  if (lines[0] !== "---") {
    return [];
  }

  const bumps = [];
  for (let i = 1; i < lines.length; i += 1) {
    const line = lines[i];
    if (line === "---") {
      break;
    }
    const match = line.match(/^\s*["']?([^"']+?)["']?\s*:\s*(patch|minor|major)\s*$/);
    if (match) {
      bumps.push(match[1]);
    }
  }
  return bumps;
}

const pending = [];
for (const entry of readdirSync(changesetDir)) {
  if (!entry.endsWith(".md") || entry === "README.md") {
    continue;
  }
  const changesetFile = path.join(changesetDir, entry);
  for (const packageName of changesetPackageBumps(changesetFile)) {
    if (targetPackages.has(packageName)) {
      pending.push({
        packageName,
        file: path.relative(workspaceRoot, changesetFile),
      });
    }
  }
}

if (pending.length > 0) {
  console.error("unapplied publishable package Changesets remain; run `vp changeset:version` before release-ready checks:");
  for (const item of pending) {
    const pkg = publishablePackages.get(item.packageName);
    console.error(`  ${item.file} -> ${item.packageName}@${pkg.version}`);
  }
  process.exit(1);
}

process.stdout.write("release hygiene ok\n");
