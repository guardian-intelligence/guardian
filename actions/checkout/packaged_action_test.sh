#!/usr/bin/env bash
# GitHub runs the action from a downloaded repository tree. Exercise that
# committed entrypoint under the real root package scope with the pinned Node.
set -euo pipefail

"$1" --input-type=module - "${@:2}" <<'JS'
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';

const [manifestPath, bundlePath, packagePath] = process.argv.slice(2);
const manifest = readFileSync(manifestPath, 'utf8');
assert.equal(Number(process.versions.node.split('.')[0]), 24, 'exercise the declared Node 24 runtime');
assert.match(manifest, /^  using: node24$/m);
const main = manifest.match(/^  main: (\S+)$/m)?.[1];
assert.ok(main && /^dist\/[^/]+$/.test(main), 'manifest must name a packaged entrypoint');

const root = mkdtempSync(join(process.env.TEST_TMPDIR ?? tmpdir(), 'packaged-checkout-'));
try {
  const entrypoint = join(root, 'actions', 'checkout', main);
  mkdirSync(dirname(entrypoint), { recursive: true });
  copyFileSync(bundlePath, entrypoint);
  copyFileSync(packagePath, join(root, 'package.json'));
  const workspace = join(root, 'workspace');
  mkdirSync(workspace);
  writeFileSync(join(workspace, 'durable-cache'), 'preserve me\n');
  const baseEnvironment = { HOME: root, GITHUB_WORKSPACE: workspace };
  const run = (environment, expectedError) => {
    const result = spawnSync(process.execPath, [entrypoint], {
      cwd: workspace,
      env: { ...baseEnvironment, ...environment },
      encoding: 'utf8',
      timeout: 10000,
    });
    assert.ifError(result.error);
    assert.equal(result.signal, null);
    assert.equal(result.status, 1, 'the real action should reject the invalid configuration');
    assert.equal(result.stderr, '', 'Node must load the packaged modules without a loader failure');
    assert.ok(result.stdout.includes(`::error::Postflight checkout runtime configuration is invalid: ${expectedError} [phase=Received, impact=TargetUntouched]`), result.stdout);
    assert.deepEqual(readdirSync(workspace), ['durable-cache']);
    assert.equal(readFileSync(join(workspace, 'durable-cache'), 'utf8'), 'preserve me\n');
  };
  // Configuration loading happens after the bundle's imports and live adapters
  // initialize. This reached an ESM loader exception before the packaging fix.
  run({}, 'one or more required environment variables are missing');
  // Also reach input validation and token masking, rejecting the unsupported
  // origin before target preparation, Git execution, or any network request.
  run({
    INPUT_REPOSITORY: 'guardian-intelligence/guardian',
    INPUT_REF: 'refs/heads/main',
    GITHUB_SHA: '0123456789abcdef0123456789abcdef01234567',
    POSTFLIGHT_ATTEMPT_ID: 'packaging-test-attempt',
    POSTFLIGHT_EXECUTION_ID: 'packaging-test-execution',
    POSTFLIGHT_CHECKOUT_TOKEN: 'inert-packaging-test-value',
    POSTFLIGHT_HOST_SERVICE_HTTP_ORIGIN: 'http://127.0.0.1:1',
  }, 'checkout control-plane origin or path is invalid');
} finally {
  rmSync(root, { recursive: true, force: true });
}
JS
