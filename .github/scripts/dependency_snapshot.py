#!/usr/bin/env python3
"""Bind Syft's dependency snapshot to the reviewed commit and repository paths.

This command does not publish or read credentials. Scan a clean git archive,
then give its output to this command before submitting through GitHub's API.
"""

import argparse
import copy
import json
from pathlib import Path
import re


MANIFESTS = frozenset(
    {
        "go.mod",
        "src/tools/hauler/go.mod",
        "pnpm-lock.yaml",
        "src/guardian-bench/uv.lock",
        "src/postflight/cli/Cargo.lock",
    }
)


def normalize(snapshot, source_root, sha, ref, job_id):
    """Keep supported manifests complete and correct Syft's dependency-of edges."""
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("sha must be an exact lowercase Git commit")
    if ref != "refs/heads/main":
        raise ValueError("dependency publication is restricted to reviewed main")
    if not re.fullmatch(r"[0-9]+/[0-9]+", job_id):
        raise ValueError("job ID must be the workflow run ID and attempt")
    if snapshot.get("version") != 0 or not snapshot.get("scanned"):
        raise ValueError("invalid dependency snapshot envelope")
    detector = snapshot.get("detector", {})
    if detector.get("name") != "syft" or detector.get("version") != "1.51.1":
        raise ValueError("snapshot must come from the reviewed Syft 1.51.1 exporter")

    root = Path(source_root).resolve()
    manifests = {}
    for manifest in snapshot.get("manifests", {}).values():
        location = manifest.get("file", {}).get("source_location", "")
        if not location:
            raise ValueError("manifest has no source location")
        source = Path(location)
        source = source if source.is_absolute() else root / source
        try:
            relative = source.resolve().relative_to(root).as_posix()
        except ValueError as error:
            raise ValueError("manifest source escapes the scanned archive") from error
        # Static GitHub action analysis remains authoritative: Syft represents
        # those as pkg:github, while GitHub uses pkg:githubactions. Empty
        # Terraform manifests are not a reason to replace security metadata.
        if relative not in MANIFESTS:
            continue
        if relative in manifests:
            raise ValueError(f"duplicate dependency manifest: {relative}")
        resolved = manifest.get("resolved", {})
        if not isinstance(resolved, dict) or not resolved:
            raise ValueError(f"dependency manifest is empty: {relative}")
        for key, dependency in resolved.items():
            if not dependency.get("package_url", "").startswith("pkg:"):
                raise ValueError(f"dependency has no package URL: {relative}: {key}")
            missing = set(dependency.get("dependencies", [])) - resolved.keys() - {""}
            if missing:
                raise ValueError(f"dependency edges are unresolved: {relative}: {key}")
        manifest = copy.deepcopy(manifest)
        # Syft 1.51.1's github-json exporter emits dependency-of edges in the
        # opposite direction to GitHub's dependencies field and marks every
        # package direct. Reverse known edges; omit unsupported directness
        # claims. An empty target is Syft's local root without a package URL.
        for dependency in manifest["resolved"].values():
            dependency.pop("relationship", None)
            dependency["dependencies"] = []
        for child, dependency in resolved.items():
            for parent in dependency.get("dependencies", []):
                if parent:
                    manifest["resolved"][parent]["dependencies"].append(child)
        for dependency in manifest["resolved"].values():
            dependency["dependencies"] = sorted(set(dependency["dependencies"]))
        manifest["name"] = relative
        manifest["file"]["source_location"] = relative
        manifests[relative] = manifest
    missing = MANIFESTS - manifests.keys()
    if missing:
        raise ValueError("scan omitted dependency manifests: " + ", ".join(sorted(missing)))

    return {
        "version": 0,
        "sha": sha,
        "ref": ref,
        "job": {"correlator": "guardian-images-dependencies", "id": job_id},
        "detector": {
            "name": "guardian-syft",
            "version": "1.0.0",
            "url": "https://github.com/guardian-intelligence/guardian/blob/main/.github/scripts/dependency_snapshot.py",
        },
        "metadata": {"syft_version": detector["version"]},
        "scanned": snapshot["scanned"],
        "manifests": dict(sorted(manifests.items())),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("input", "source-root", "sha", "ref", "job-id", "output"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    try:
        snapshot = normalize(
            json.loads(Path(args.input).read_text()),
            args.source_root,
            args.sha,
            args.ref,
            args.job_id,
        )
    except (ValueError, KeyError, TypeError) as error:
        parser.exit(1, f"dependency snapshot rejected: {error}\n")
    Path(args.output).write_text(json.dumps(snapshot, indent=2) + "\n")
    print(f"Prepared {len(snapshot['manifests'])} dependency manifests for {args.sha}")


if __name__ == "__main__":
    main()
